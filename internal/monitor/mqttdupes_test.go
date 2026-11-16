package monitor

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// hassAgent builds discovery configs the way HASS.Agent publishes them: node id = PC
// name, unique ids = sensor GUIDs from its config file.
func hassAgent(msgs map[string][]byte, pc string, guids ...string) {
	for i, g := range guids {
		msgs[fmt.Sprintf("homeassistant/sensor/%s/s%d/config", pc, i)] = []byte(fmt.Sprintf(
			`{"name":"%s s%d","uniq_id":"%s","stat_t":"homeassistant/sensor/%s/s%d/state","dev":{"ids":["hass.agent-%s"],"name":"%s","mf":"HASS.Agent"}}`,
			pc, i, g, pc, i, pc, pc))
	}
}

func TestMQTTDupesClean(t *testing.T) {
	msgs := map[string][]byte{}
	hassAgent(msgs, "PC1", "a1", "a2")
	hassAgent(msgs, "PC2", "b1", "b2")
	// Same unique id in another domain is allowed (registry key is domain + id).
	msgs["homeassistant/binary_sensor/PC1/x/config"] = []byte(`{"unique_id":"a1","device":{"identifiers":"hass.agent-PC1","name":"PC1"}}`)
	// Deleted config, foreign topics, garbage.
	msgs["homeassistant/sensor/old/x/config"] = nil
	msgs["homeassistant/sensor/PC1/s0/state"] = []byte(`12`)
	msgs["homeassistant/status"] = []byte(`online`)
	msgs["homeassistant/sensor/bad/config"] = []byte(`not json`)
	// Entities without node id sharing a device are normal.
	msgs["homeassistant/sensor/plug_power/config"] = []byte(`{"uniq_id":"plug_power","dev":{"ids":["plug"],"cns":[["mac","AA:BB"]],"name":"Plug"}}`)
	msgs["homeassistant/switch/plug_relay/config"] = []byte(`{"uniq_id":"plug_relay","dev":{"ids":["plug"],"cns":[["mac","aa:bb"]],"name":"Plug"}}`)
	res := mqttDupesResult("homeassistant", msgs, nil)
	if !res.OK || !strings.Contains(res.Msg, "7 discovery entities") {
		t.Fatalf("got %+v", res)
	}
}

func TestMQTTDupesClonedConfig(t *testing.T) {
	msgs := map[string][]byte{}
	guids := make([]string, 36)
	for i := range guids {
		guids[i] = fmt.Sprintf("guid-%02d", i)
	}
	hassAgent(msgs, "PC1", guids...)
	hassAgent(msgs, "PC2", guids...) // config folder copied, PC renamed
	bad := discoveryDupes(parseDiscovery("homeassistant", msgs), nil)
	if len(bad) != 1 || bad[0] != "36 unique ids used by PC1, PC2 (e.g. sensor guid-00)" {
		t.Fatalf("got %q", bad)
	}
	if bad := discoveryDupes(parseDiscovery("homeassistant", msgs), []string{"PC2"}); len(bad) != 0 {
		t.Fatalf("ignored node still reported: %q", bad)
	}
	if bad := discoveryDupes(parseDiscovery("homeassistant", msgs), []string{"guid-*"}); len(bad) != 0 {
		t.Fatalf("ignored ids still reported: %q", bad)
	}
}

func TestMQTTDupesSharedDevice(t *testing.T) {
	// Copied firmware: own unique ids (derived from the topic) but the same device
	// identifier and MAC on two nodes.
	msgs := map[string][]byte{
		"homeassistant/sensor/esp-kitchen/temp/config": []byte(`{"uniq_id":"kitchen_temp","dev":{"ids":"esp-a1b2","cns":[["mac","a1:b2"]],"name":"esp-kitchen"}}`),
		"homeassistant/sensor/esp-garage/temp/config":  []byte(`{"uniq_id":"garage_temp","dev":{"ids":"esp-a1b2","cns":[["mac","a1:b2"]],"name":"esp-garage"}}`),
		// No node id, same identifier, different device names.
		"homeassistant/light/lamp1/config": []byte(`{"uniq_id":"lamp1","dev":{"ids":"lamp","name":"Lamp left"}}`),
		"homeassistant/light/lamp2/config": []byte(`{"uniq_id":"lamp2","dev":{"ids":"lamp","name":"Lamp right"}}`),
	}
	bad := discoveryDupes(parseDiscovery("homeassistant", msgs), nil)
	want := []string{"device id esp-a1b2 claimed by esp-garage, esp-kitchen", "device id lamp claimed by Lamp left, Lamp right"}
	if strings.Join(bad, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", bad)
	}
}

func TestMQTTDupesDeviceBased(t *testing.T) {
	msgs := map[string][]byte{
		"homeassistant/device/node1/config": []byte(`{"dev":{"ids":"n1","name":"N1"},"o":{"name":"x"},"cmps":{"t":{"p":"sensor","uniq_id":"dup"},"h":{"p":"sensor","unique_id":"dup"},"b":{"p":"binary_sensor","uniq_id":"dup"}}}`),
	}
	bad := discoveryDupes(parseDiscovery("homeassistant", msgs), nil)
	if len(bad) != 1 || !strings.HasPrefix(bad[0], "unique id sensor dup used by homeassistant/device/node1/config#h, homeassistant/device/node1/config#t") {
		t.Fatalf("got %q", bad)
	}
	if r := mqttDupesResult("other", msgs, nil); r.OK || r.Msg != "no discovery configs under other/" {
		t.Fatalf("got %+v", r)
	}
}

// Against a real broker when HW_MQTT_HOST is set (anonymous, port HW_MQTT_PORT or
// 1883): publishes a cloned HASS.Agent config under a test prefix, checks, cleans up.
func TestMQTTDupesBroker(t *testing.T) {
	host := os.Getenv("HW_MQTT_HOST")
	if host == "" {
		t.Skip("HW_MQTT_HOST not set")
	}
	port, _ := strconv.Atoi(os.Getenv("HW_MQTT_PORT"))
	cfg := Config{Host: host, Port: port, Topic: "hwtest", Timeout: 15}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cl, err := mqttConnect(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Disconnect(100)
	msgs := map[string][]byte{}
	hassAgent(msgs, "PC1", "g1", "g2")
	hassAgent(msgs, "PC2", "g1", "g2")
	pub := func(topic string, p []byte) {
		if tok := cl.Publish(topic, 0, true, p); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
			t.Fatalf("publish %s: %v", topic, tok.Error())
		}
	}
	for topic, p := range msgs {
		pub(strings.Replace(topic, "homeassistant/", "hwtest/", 1), p)
	}
	defer func() {
		for topic := range msgs {
			pub(strings.Replace(topic, "homeassistant/", "hwtest/", 1), nil)
		}
	}()
	res := Check(context.Background(), "mqttdupes", cfg)
	t.Logf("ok=%v ms=%.0f %s", res.OK, res.Ms, res.Msg)
	if res.OK || res.Msg != "2 unique ids used by PC1, PC2 (e.g. sensor g1)" {
		t.Fatalf("got %+v", res)
	}
	cfg.IgnoreRefs = []string{"PC2"}
	if res := Check(context.Background(), "mqttdupes", cfg); !res.OK {
		t.Fatalf("with ignore: %+v", res)
	}
}

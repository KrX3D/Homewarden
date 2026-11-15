package monitor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/KrX3D/Homewarden/internal/store"
)

func decode(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func keys(m map[string]bool) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestHAWalk(t *testing.T) {
	r := newHARefs()
	haWalk(decode(t, `{
		"type": "custom:bar-card",
		"entities": [{"entity": "sensor.krx_unraid_cpu"}, "sensor.krx_smart_sensor_sda"],
		"tap_action": {"action": "call-service", "service": "light.turn_on", "perform_action": "switch.toggle"},
		"image": "/local/person.jpg",
		"icon": "mdi:light.bulb",
		"label": "[[[ return states['sensor.krx_unraid_temp'].state + ' C' ]]]",
		"value_template": "{{ states.sensor.outside_temp.state | float > 3 and is_state('input_boolean.away', 'on') }}",
		"dyn": "{{ states('sensor.' ~ name) }} sensor.part_{{ x }} trigger.to_state.state hw@light.example camera.entity_id",
		"device_id": "abc123",
		"target": {"area_id": ["kitchen"], "device_id": ["{{ d }}"], "entity_id": "0123456789abcdef0123456789abcdef"}
	}`), "", r)
	if got := strings.Join(keys(r.entities), " "); got != "input_boolean.away sensor.krx_smart_sensor_sda sensor.krx_unraid_cpu sensor.krx_unraid_temp sensor.outside_temp trigger.to_state" {
		t.Errorf("entities: %s", got)
	}
	if got := strings.Join(keys(r.cards), " "); got != "custom:bar-card" {
		t.Errorf("cards: %s", got)
	}
	if got := strings.Join(keys(r.devices), " "); got != "abc123" {
		t.Errorf("devices: %s", got)
	}
	if !r.areas["kitchen"] || !r.entityUIDs["0123456789abcdef0123456789abcdef"] {
		t.Errorf("areas %v uids %v", r.areas, r.entityUIDs)
	}
}

func TestHACardProvided(t *testing.T) {
	res := []haResource{{URL: "/hacsfiles/button-card/button-card.js"}, {URL: "/hacsfiles/lovelace-mushroom/mushroom.js?hacstag=1"}, {URL: "/hacsfiles/lovelace-layout-card/layout-card.js"}, {URL: "/hacsfiles/lovelace-card-mod/card-mod.js"}}
	for card, want := range map[string]bool{"custom:button-card": true, "custom:mushroom-light-card": true, "custom:grid-layout": true, "custom:mod-card": true, "custom:bar-card": false, "custom:mini-graph-card": false} {
		if got := haCardProvided(card, res); got != want {
			t.Errorf("%s: got %v", card, got)
		}
	}
}

func fakeHAConfig(t *testing.T) haConfigData {
	return haConfigData{
		entities:  map[string]bool{"sensor.cpu": true, "light.kitchen": true, "automation.night": true, "person.krx": true},
		entityUID: map[string]bool{"0123456789abcdef0123456789abcdef": true},
		devices:   map[string]bool{"dev1": true},
		areas:     map[string]bool{"kitchen": true},
		resources: []haResource{{URL: "/hacsfiles/button-card/button-card.js"}, {URL: "/hacsfiles/bar-card/bar-card.js"}},
		resOK:     true,
		resStatus: map[string]int{"/hacsfiles/button-card/button-card.js": 200, "/hacsfiles/bar-card/bar-card.js": 404},
		sources: []haSource{
			{"dashboard Server/templates", decode(t, `{"button_card_templates": {"disk": {"label": "[[[ return states['sensor.krx_smart_sensor_sda'].state ]]]"}}}`)},
			{"dashboard Server/Disks", decode(t, `{"cards": [{"type": "custom:bar-card", "entity": "sensor.krx_unraid_cpu"}, {"type": "custom:apexcharts-card", "entity": "sensor.cpu"}, {"type": "picture", "image": "/local/person.jpg"}]}`)},
			{"automation Night", decode(t, `{"triggers": [{"trigger": "device", "device_id": "gone", "entity_id": "ffffffffffffffffffffffffffffffff"}], "actions": [{"action": "light.turn_off", "target": {"entity_id": ["light.kitchen", "light.old"], "area_id": "attic"}}, {"action": "notify.phone", "data": {"message": "bye.bye from widget.x"}}]}`)},
			{"script Fine", decode(t, `{"sequence": [{"action": "light.turn_on", "target": {"entity_id": "light.kitchen", "device_id": "dev1", "area_id": "kitchen"}}]}`)},
		},
	}
}

func TestHAConfigResult(t *testing.T) {
	got := haConfigFindings(fakeHAConfig(t), Config{})
	want := []string{
		"dashboard Server/templates: sensor.krx_smart_sensor_sda",
		"dashboard Server/Disks: sensor.krx_unraid_cpu",
		"automation Night: area attic, device gone, entity ffffffffffffffffffffffffffffffff +1",
		"custom:apexcharts-card has no resource (dashboard Server/Disks)",
		"resource /hacsfiles/bar-card/bar-card.js not found",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got\n%s", strings.Join(got, "\n"))
	}
	res := haConfigResult(fakeHAConfig(t), Config{})
	if res.OK || !strings.HasPrefix(res.Msg, "5 broken: dashboard Server/templates: sensor.krx_smart_sensor_sda") {
		t.Errorf("result %v %q", res.OK, res.Msg)
	}
	res = haConfigResult(fakeHAConfig(t), Config{IgnoreRefs: []string{"sensor.KRX_*", "custom:apex*", "/hacsfiles/bar-card/*", "attic", "gone", "f*", "light.old"}})
	if !res.OK || res.Msg != "4 dashboards/automations/scripts, no broken references" {
		t.Errorf("ignored: %v %q", res.OK, res.Msg)
	}
	d := fakeHAConfig(t)
	d.resOK, d.areas = false, nil // non-admin: no resource list, no areas
	for _, l := range haConfigFindings(d, Config{}) {
		if strings.Contains(l, "custom:") || strings.Contains(l, "area ") {
			t.Errorf("unexpected without lists: %s", l)
		}
	}
}

// TestHAConfigFake runs the whole check against a fake WebSocket API and static files.
func TestHAConfigFake(t *testing.T) {
	results := map[string]any{
		"config/device_registry/list": []map[string]any{{"id": "dev1", "config_entries": []string{}}},
		"config/entity_registry/list": []map[string]any{{"id": "u1", "entity_id": "sensor.cpu"}},
		"get_states": []map[string]any{
			{"entity_id": "sensor.cpu"},
			{"entity_id": "automation.night", "attributes": map[string]any{"friendly_name": "Night", "id": "1"}},
			{"entity_id": "script.yaml_only", "attributes": map[string]any{}},
		},
		"config/area_registry/list": []map[string]any{{"area_id": "kitchen"}},
		"lovelace/dashboards/list":  []map[string]any{{"url_path": "server", "title": "Server", "mode": "storage"}},
		"lovelace/resources":        []map[string]any{{"url": "/hacsfiles/bar-card/bar-card.js", "type": "module"}, {"url": "https://cdn.example/x.js", "type": "module"}},
	}
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/websocket" {
			w.WriteHeader(http.StatusNotFound) // bar-card.js was deleted
			return
		}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		c.WriteJSON(map[string]string{"type": "auth_required"})
		var auth map[string]string
		if c.ReadJSON(&auth) != nil || auth["access_token"] != "tok" {
			c.WriteJSON(map[string]string{"type": "auth_invalid"})
			return
		}
		c.WriteJSON(map[string]string{"type": "auth_ok"})
		for {
			var cmd map[string]any
			if c.ReadJSON(&cmd) != nil {
				return
			}
			id := cmd["id"]
			var res any
			switch cmd["type"] {
			case "lovelace/config":
				if cmd["url_path"] == nil { // auto-generated default dashboard
					c.WriteJSON(map[string]any{"id": id, "type": "result", "success": false, "error": map[string]string{"code": "config_not_found"}})
					continue
				}
				res = map[string]any{"views": []any{map[string]any{"title": "Disks", "cards": []any{map[string]any{"type": "custom:bar-card", "entity": "sensor.krx_unraid_cpu"}}}}}
			case "automation/config":
				res = map[string]any{"config": map[string]any{"actions": []any{map[string]any{"action": "light.turn_on", "target": map[string]any{"entity_id": "sensor.cpu", "device_id": "dev1"}}}}}
			case "script/config":
				c.WriteJSON(map[string]any{"id": id, "type": "result", "success": false, "error": map[string]string{"code": "not_found"}})
				continue
			default:
				res = results[cmd["type"].(string)]
			}
			c.WriteJSON(map[string]any{"id": id, "type": "result", "success": true, "result": res})
		}
	}))
	defer srv.Close()
	st, err := store.Open(t.TempDir() + "/hw.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	cfg, _ := json.Marshal(map[string]any{"settings": map[string]any{"token": 1, "url": srv.URL}})
	d, err := st.CreateDevice(ctx, store.Device{Name: "HA", Kind: "home-assistant", Host: "127.0.0.1", Status: "active", Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	r := NewRunner(st, nil)
	r.Creds = func(context.Context, int64) (string, string, error) { return "", "tok", nil }
	res := r.Test(ctx, "haconfig", Config{}, &d.ID)
	want := "2 broken: dashboard Server/Disks: sensor.krx_unraid_cpu; resource /hacsfiles/bar-card/bar-card.js not found"
	if res.OK || res.Msg != want {
		t.Fatalf("got %v %q", res.OK, res.Msg)
	}
	res = r.Test(ctx, "haconfig", Config{IgnoreRefs: []string{"sensor.krx_*", "/hacsfiles/bar-card/*"}}, &d.ID)
	if !res.OK || !strings.HasPrefix(res.Msg, "3 dashboards") {
		t.Fatalf("ignored: %v %q", res.OK, res.Msg)
	}
}

// TestHAConfigLive checks a real Home Assistant (read only):
//
//	HW_LIVE=1 HA_URL=https://ha.example HA_TOKEN=... go test ./internal/monitor -run HAConfigLive -v
func TestHAConfigLive(t *testing.T) {
	tok, u := os.Getenv("HA_TOKEN"), os.Getenv("HA_URL")
	if os.Getenv("HW_LIVE") != "1" || tok == "" || u == "" {
		t.Skip("HW_LIVE=1, HA_URL and HA_TOKEN needed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	d, err := haFetchConfig(ctx, haTarget{base: strings.TrimRight(u, "/"), token: tok})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d entities, %d devices, %d resources (list ok %v), %d sources", len(d.entities), len(d.devices), len(d.resources), d.resOK, len(d.sources))
	for _, l := range haConfigFindings(d, Config{}) {
		t.Log(l)
	}
}

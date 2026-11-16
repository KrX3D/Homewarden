package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// Duplicate MQTT discovery ids (type mqttdupes): reads the retained Home Assistant
// discovery configs from the broker (<prefix>/<component>/[<node_id>/]<object_id>/config,
// also device-based configs with "cmps") and finds
//   - unique ids used by more than one config of the same entity domain (Home Assistant
//     ignores all but the first and logs "already exists"), and
//   - device identifiers or connections claimed by configs of different nodes or with
//     different device names (a cloned HASS.Agent config or firmware copy: Home
//     Assistant silently merges both into one device).
// Config: Topic = discovery prefix (empty = homeassistant), IgnoreRefs = unique ids,
// device ids, node ids or topics to skip (path.Match patterns).

// discoveryConfig is one entity found in a discovery config.
type discoveryConfig struct {
	topic, node, domain, uniqueID, devName string
	devIDs                                 []string // "id:<identifier>" and "<type>:<value>" connections
}

// parseDiscovery turns retained messages into entities; topics outside
// <prefix>/.../config and empty or non-JSON payloads are skipped.
func parseDiscovery(prefix string, msgs map[string][]byte) []discoveryConfig {
	var out []discoveryConfig
	for topic, payload := range msgs {
		rest, ok := strings.CutPrefix(topic, prefix+"/")
		if !ok || len(payload) == 0 {
			continue
		}
		parts := strings.Split(rest, "/")
		if parts[len(parts)-1] != "config" || len(parts) < 3 || len(parts) > 4 {
			continue
		}
		node := ""
		if len(parts) == 4 {
			node = parts[1]
		}
		var p map[string]any
		if json.Unmarshal(payload, &p) != nil {
			continue
		}
		devName, devIDs := discoveryDevice(p)
		base := discoveryConfig{topic: topic, node: node, devName: devName, devIDs: devIDs}
		if cmps, ok := pick(p, "cmps", "components").(map[string]any); ok {
			// Device-based discovery: one config, several components with their platform.
			for key, v := range cmps {
				c, _ := v.(map[string]any)
				if c == nil {
					continue
				}
				e := base
				e.topic = topic + "#" + key
				e.domain = str(pick(c, "p", "platform"))
				e.uniqueID = str(pick(c, "uniq_id", "unique_id"))
				out = append(out, e)
			}
			continue
		}
		e := base
		e.domain = parts[0]
		e.uniqueID = str(pick(p, "uniq_id", "unique_id"))
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].topic < out[j].topic })
	return out
}

func pick(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			return v
		}
	}
	return nil
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return fmt.Sprint(x)
	}
	return ""
}

// discoveryDevice reads the device block: name and its identifiers/connections.
func discoveryDevice(p map[string]any) (string, []string) {
	d, _ := pick(p, "dev", "device").(map[string]any)
	if d == nil {
		return "", nil
	}
	var ids []string
	switch x := pick(d, "ids", "identifiers").(type) {
	case string:
		ids = append(ids, "id:"+x)
	case []any:
		for _, v := range x {
			if s := str(v); s != "" {
				ids = append(ids, "id:"+s)
			}
		}
	}
	if cns, ok := pick(d, "cns", "connections").([]any); ok {
		for _, c := range cns {
			if pair, ok := c.([]any); ok && len(pair) == 2 {
				ids = append(ids, strings.ToLower(str(pair[0])+":"+str(pair[1])))
			}
		}
	}
	return str(d["name"]), ids
}

// owner names where a config comes from: its node id, else its topic.
func (e discoveryConfig) owner() string {
	if e.node != "" {
		return e.node
	}
	return e.topic
}

func ignoredBy(patterns []string, values ...string) bool {
	for _, p := range patterns {
		for _, v := range values {
			if v == "" {
				continue
			}
			if ok, _ := path.Match(p, v); ok || p == v {
				return true
			}
		}
	}
	return false
}

// discoveryDupes returns the findings: duplicate unique ids grouped by the set of
// nodes that share them, then shared device ids.
func discoveryDupes(ents []discoveryConfig, ignore []string) []string {
	var out []string

	// Unique ids per domain.
	byUID := map[string][]discoveryConfig{}
	for _, e := range ents {
		if e.uniqueID == "" || ignoredBy(ignore, e.uniqueID, e.node, e.topic) {
			continue
		}
		k := e.domain + "\x00" + e.uniqueID
		byUID[k] = append(byUID[k], e)
	}
	type group struct {
		count   int
		example string
	}
	groups := map[string]*group{}
	for _, list := range byUID {
		if len(list) < 2 {
			continue
		}
		owners := map[string]bool{}
		for _, e := range list {
			owners[e.owner()] = true
		}
		names := setKeys(owners)
		key := strings.Join(names, ", ")
		if len(names) == 1 {
			key = names[0] + " (twice)"
		}
		g := groups[key]
		if g == nil {
			g = &group{}
			groups[key] = g
		}
		g.count++
		ex := list[0].domain + " " + list[0].uniqueID
		if g.example == "" || ex < g.example {
			g.example = ex
		}
	}
	for _, key := range sortedKeys(groups) {
		g := groups[key]
		if g.count == 1 {
			out = append(out, fmt.Sprintf("unique id %s used by %s", g.example, key))
		} else {
			out = append(out, fmt.Sprintf("%d unique ids used by %s (e.g. %s)", g.count, key, g.example))
		}
	}

	// Device identifiers: more than one node or device name behind one id.
	type devUse struct{ nodes, names map[string]bool }
	byDev := map[string]*devUse{}
	for _, e := range ents {
		for _, id := range e.devIDs {
			bare := strings.TrimPrefix(id, "id:")
			if ignoredBy(ignore, bare, id, e.node, e.topic) {
				continue
			}
			u := byDev[id]
			if u == nil {
				u = &devUse{nodes: map[string]bool{}, names: map[string]bool{}}
				byDev[id] = u
			}
			if e.node != "" {
				u.nodes[e.node] = true
			}
			if e.devName != "" {
				u.names[e.devName] = true
			}
		}
	}
	seen := map[string]bool{}
	for _, id := range sortedKeys(byDev) {
		u := byDev[id]
		var who []string
		switch {
		case len(u.nodes) > 1:
			who = setKeys(u.nodes)
		case len(u.names) > 1:
			who = setKeys(u.names)
		default:
			continue
		}
		// A cloned device usually repeats several ids (identifier + MAC): report once.
		k := strings.Join(who, "\x00")
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, fmt.Sprintf("device id %s claimed by %s", strings.TrimPrefix(id, "id:"), strings.Join(who, ", ")))
	}
	return out
}

func setKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func discoveryPrefix(c Config) string {
	p := strings.Trim(strings.TrimSpace(c.Topic), "/")
	if p == "" {
		return "homeassistant"
	}
	return p
}

func mqttDupesResult(prefix string, msgs map[string][]byte, ignore []string) Result {
	ents := parseDiscovery(prefix, msgs)
	if len(ents) == 0 {
		return Result{Msg: "no discovery configs under " + prefix + "/"}
	}
	if bad := discoveryDupes(ents, ignore); len(bad) > 0 {
		return Result{Msg: short(strings.Join(bad, "; "))}
	}
	return Result{OK: true, Msg: fmt.Sprintf("%d discovery entities, no duplicate ids", len(ents))}
}

// checkMQTTDupes collects the retained configs below the discovery prefix (until the
// broker is quiet for 2 s or the timeout ends) and checks them.
func checkMQTTDupes(ctx context.Context, c Config) Result {
	start := time.Now()
	cl, err := mqttConnect(ctx, c)
	if err != nil {
		return Result{Msg: err.Error()}
	}
	defer cl.Disconnect(100)
	prefix := discoveryPrefix(c)
	var mu sync.Mutex
	msgs := map[string][]byte{}
	last := time.Now()
	sub := cl.Subscribe(prefix+"/#", 0, func(_ mqtt.Client, m mqtt.Message) {
		mu.Lock()
		msgs[m.Topic()] = append([]byte(nil), m.Payload()...)
		last = time.Now()
		mu.Unlock()
	})
	deadline, _ := ctx.Deadline()
	if !sub.WaitTimeout(time.Until(deadline)) || sub.Error() != nil {
		return Result{Msg: fmt.Sprintf("subscribe %s/# failed: %v", prefix, sub.Error())}
	}
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
wait:
	for {
		select {
		case <-ctx.Done():
			break wait
		case <-tick.C:
			mu.Lock()
			quiet := time.Since(last) >= 2*time.Second
			mu.Unlock()
			if quiet {
				break wait
			}
		}
	}
	cl.Unsubscribe(prefix + "/#")
	mu.Lock()
	got := msgs
	msgs = map[string][]byte{}
	mu.Unlock()
	res := mqttDupesResult(prefix, got, c.IgnoreRefs)
	res.Ms = float64(time.Since(start).Microseconds()) / 1000
	return res
}

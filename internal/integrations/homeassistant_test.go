package integrations

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeHA serves /api/config, /api/states and update.install like Home Assistant.
type fakeHA struct {
	mu       sync.Mutex
	entities map[string]map[string]any
	installs []map[string]any
	backup   string
}

func newFakeHA() *fakeHA {
	ent := func(id, state, inst, latest, pic string, features int) map[string]any {
		return map[string]any{"entity_id": id, "state": state, "attributes": map[string]any{
			"installed_version": inst, "latest_version": latest, "supported_features": features, "entity_picture": pic,
			"title": strings.TrimPrefix(id, "update."), "release_url": "https://github.com/home-assistant/core/releases/tag/" + latest,
		}}
	}
	return &fakeHA{backup: "idle", entities: map[string]map[string]any{
		"update.home_assistant_core_update":             ent("update.home_assistant_core_update", "on", "2026.10.1", "2026.11.0", "", 1|8|16),
		"update.home_assistant_operating_system_update": ent("update.home_assistant_operating_system_update", "off", "16.2", "16.2", "", 1),
		"update.mosquitto_broker_update":                ent("update.mosquitto_broker_update", "on", "6.4.1", "6.5.0", "/api/hassio/addons/core_mosquitto/icon", 1|8),
		"update.hacs_update":                            ent("update.hacs_update", "off", "2.0.1", "2.0.1", "https://brands.home-assistant.io/_/hacs/icon.png", 1),
	}}
}

func (f *fakeHA) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer secret-uuid" {
		w.WriteHeader(401)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.URL.Path == "/api/config":
		json.NewEncoder(w).Encode(map[string]any{"version": "2026.10.1", "location_name": "Home", "state": "RUNNING"})
	case r.URL.Path == "/api/states":
		out := []any{map[string]any{"entity_id": "light.kitchen", "state": "on"}}
		for _, e := range f.entities {
			out = append(out, e)
		}
		json.NewEncoder(w).Encode(out)
	case r.URL.Path == "/api/states/sensor.backup_backup_manager_state":
		json.NewEncoder(w).Encode(map[string]any{"state": f.backup})
	case strings.HasPrefix(r.URL.Path, "/api/states/"):
		e, ok := f.entities[strings.TrimPrefix(r.URL.Path, "/api/states/")]
		if !ok {
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(e)
	case r.URL.Path == "/api/services/update/install" && r.Method == "POST":
		var body map[string]any
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		f.installs = append(f.installs, body)
		e := f.entities[body["entity_id"].(string)]
		a := e["attributes"].(map[string]any)
		a["installed_version"] = a["latest_version"]
		e["state"] = "off"
		w.Write([]byte("[]"))
	default:
		w.WriteHeader(404)
	}
}

func TestHomeAssistant(t *testing.T) {
	fake := newFakeHA()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	set := func() map[string]any { return map[string]any{"backupBeforeUpdate": true} }
	fake.entities["update.orphaned_firmware"] = map[string]any{"entity_id": "update.orphaned_firmware", "state": "unavailable", "attributes": map[string]any{"friendly_name": "Orphaned firmware"}}
	fake.entities["sensor.door_battery"] = map[string]any{"entity_id": "sensor.door_battery", "state": "12", "attributes": map[string]any{"device_class": "battery", "unit_of_measurement": "%", "friendly_name": "Door battery"}}
	fake.entities["sensor.remote_battery"] = map[string]any{"entity_id": "sensor.remote_battery", "state": "80", "attributes": map[string]any{"device_class": "battery", "unit_of_measurement": "%"}}
	fake.entities["binary_sensor.smoke_battery_low"] = map[string]any{"entity_id": "binary_sensor.smoke_battery_low", "state": "on", "attributes": map[string]any{"device_class": "battery", "friendly_name": "Smoke detector"}}

	out, err := runFn(t, "home-assistant", srv, set(), "detect")
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Facts      map[string]any   `json:"facts"`
		Components []map[string]any `json:"components"`
	}
	json.Unmarshal(out, &res)
	if len(res.Components) != 4 || res.Facts["updates"] != float64(2) || res.Facts["version"] != "2026.10.1" {
		t.Fatalf("detect: %+v", res)
	}
	if low, _ := json.Marshal(res.Facts["lowBatteries"]); string(low) != `[{"entity":"binary_sensor.smoke_battery_low","level":null,"name":"Smoke detector"},{"entity":"sensor.door_battery","level":12,"name":"Door battery"}]` {
		t.Fatalf("low batteries: %s", low)
	}
	byID := map[string]map[string]any{}
	for _, c := range res.Components {
		byID[c["id"].(string)] = c
	}
	core := byID["ha:update.home_assistant_core_update"]
	if core["group"] != "app" || core["available"] != "2026.11.0" || core["updatable"] != true {
		t.Fatalf("core: %v", core)
	}
	if byID["ha:update.home_assistant_operating_system_update"]["group"] != "os" {
		t.Fatal("OS group")
	}
	if m := byID["ha:update.mosquitto_broker_update"]["meta"].(map[string]any); m["kind"] != "addon" {
		t.Fatalf("addon kind: %v", m)
	}
	if res.Components[0]["id"] != "ha:update.home_assistant_core_update" {
		t.Fatal("core should be listed first")
	}

	fake.backup = "create_backup"
	out, _ = runFn(t, "home-assistant", srv, set(), "busy")
	if !strings.Contains(string(out), `"busy":true`) {
		t.Fatalf("busy during backup: %s", out)
	}
	fake.backup = "idle"

	if _, err := runFn(t, "home-assistant", srv, set(), "update", []string{"*"}); err != nil {
		t.Fatal(err)
	}
	if len(fake.installs) != 2 || fake.installs[0]["entity_id"] != "update.mosquitto_broker_update" || fake.installs[1]["entity_id"] != "update.home_assistant_core_update" {
		t.Fatalf("add-ons first, then Core: %v", fake.installs)
	}
	if fake.installs[1]["backup"] != true {
		t.Fatalf("backup flag: %v", fake.installs[1])
	}
}

package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/KrX3D/Homewarden/internal/store"
)

func strp(s string) *string { return &s }

// fakeRegistry: nmap with 40 nameless devices and 1 loaded entity, zha with 5 devices
// and 12 entities, unifi with 30 devices (10 nameless) and 11 loaded entities plus one
// restored placeholder.
func fakeRegistry() haRegistry {
	var reg haRegistry
	reg.entries = []haConfigEntry{{"e_nmap", "nmap_tracker"}, {"e_zha", "zha"}, {"e_unifi", "unifi"}}
	for i := 0; i < 40; i++ {
		reg.devices = append(reg.devices, haRegDevice{ID: fmt.Sprint("n", i), ConfigEntries: []string{"e_nmap"}})
	}
	for i := 0; i < 5; i++ {
		reg.devices = append(reg.devices, haRegDevice{ID: fmt.Sprint("z", i), Name: strp("Plug"), ConfigEntries: []string{"e_zha"}})
	}
	for i := 0; i < 30; i++ {
		d := haRegDevice{ID: fmt.Sprint("u", i), ConfigEntries: []string{"e_unifi"}}
		if i >= 10 {
			d.Name = strp("")
			d.NameByUser = strp("Client")
		}
		reg.devices = append(reg.devices, d)
	}
	add := func(id, entry, platform string, live, restored bool) {
		reg.entities = append(reg.entities, haRegEntity{EntityID: id, ConfigEntryID: strp(entry), Platform: platform})
		if live {
			s := haState{EntityID: id}
			s.Attributes.Restored = restored
			reg.states = append(reg.states, s)
		}
	}
	add("device_tracker.n0", "e_nmap", "nmap_tracker", true, false)
	add("device_tracker.n1", "e_nmap", "nmap_tracker", false, false) // disabled
	for i := 0; i < 12; i++ {
		add(fmt.Sprint("sensor.z", i), "e_zha", "zha", true, false)
	}
	for i := 0; i < 11; i++ {
		add(fmt.Sprint("device_tracker.u", i), "e_unifi", "unifi", true, false)
	}
	add("device_tracker.u_old", "e_unifi", "unifi", true, true)
	return reg
}

func TestHARegistryCounts(t *testing.T) {
	got := map[string]haIntegration{}
	for _, i := range haRegistryCounts(fakeRegistry()) {
		got[i.Domain] = i
	}
	want := map[string]haIntegration{
		"nmap_tracker": {"nmap_tracker", 40, 40, 1},
		"zha":          {"zha", 5, 0, 12},
		"unifi":        {"unifi", 30, 10, 11},
	}
	for d, w := range want {
		if got[d] != w {
			t.Errorf("%s: got %+v want %+v", d, got[d], w)
		}
	}
	// Without config_entries/get the domain comes from the entities' platform.
	reg := fakeRegistry()
	reg.entries = nil
	if c := haRegistryCounts(reg); c[0].Domain != "nmap_tracker" || c[0].Devices != 40 {
		t.Errorf("fallback: %+v", c[0])
	}
}

func TestHARegistryResult(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		ok   bool
		want string
	}{
		{"defaults", Config{}, false, "nmap_tracker 40 devices for 1 loaded entities (40 without a name)"},
		{"ignored", Config{IgnoreIntegrations: []string{" NMAP_tracker "}}, true, "75 devices in 3 integrations, highest unifi 2.7"},
		{"lower ratio", Config{IgnoreIntegrations: []string{"nmap_tracker"}, MaxRatio: 2}, false, "unifi 30 devices for 11 loaded entities (10 without a name)"},
		{"min devices", Config{MinDevices: 50}, true, "75 devices in 3 integrations"},
	}
	for _, tc := range cases {
		r := haRegistryResult(fakeRegistry(), tc.cfg)
		if r.OK != tc.ok || !strings.Contains(r.Msg, tc.want) {
			t.Errorf("%s: got %v %q", tc.name, r.OK, r.Msg)
		}
	}
	if r := haRegistryResult(fakeRegistry(), Config{}); strings.Contains(r.Msg, "unifi") {
		t.Errorf("unifi flagged at ratio 2.7? %q", r.Msg)
	}
}

// TestHARegistryFake runs the check against a fake WebSocket API.
func TestHARegistryFake(t *testing.T) {
	reg := fakeRegistry()
	results := map[string]any{
		"config/device_registry/list": reg.devices,
		"config/entity_registry/list": reg.entities,
		"get_states":                  reg.states,
	}
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/websocket" {
			w.WriteHeader(http.StatusNotFound)
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
			var cmd struct {
				ID   int    `json:"id"`
				Type string `json:"type"`
			}
			if c.ReadJSON(&cmd) != nil {
				return
			}
			res, ok := results[cmd.Type]
			if !ok { // config_entries/get refused: non-admin token
				c.WriteJSON(map[string]any{"id": cmd.ID, "type": "result", "success": false, "error": map[string]string{"code": "unauthorized"}})
				continue
			}
			c.WriteJSON(map[string]any{"id": cmd.ID, "type": "result", "success": true, "result": res})
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
	res := r.Test(ctx, "haregistry", Config{}, &d.ID)
	if res.OK || !strings.Contains(res.Msg, "nmap_tracker 40 devices for 1 loaded") {
		t.Fatalf("got %v %q", res.OK, res.Msg)
	}
	r.Creds = func(context.Context, int64) (string, string, error) { return "", "wrong", nil }
	if res = r.Test(ctx, "haregistry", Config{}, &d.ID); res.OK || !strings.Contains(res.Msg, "refused") {
		t.Fatalf("bad token: %v %q", res.OK, res.Msg)
	}
}

// TestHARegistryLive reads the registries of a real Home Assistant (read only):
//
//	HW_LIVE=1 HA_URL=https://ha.example HA_TOKEN=... go test ./internal/monitor -run HARegistryLive -v
func TestHARegistryLive(t *testing.T) {
	tok, u := os.Getenv("HA_TOKEN"), os.Getenv("HA_URL")
	if os.Getenv("HW_LIVE") != "1" || tok == "" || u == "" {
		t.Skip("HW_LIVE=1, HA_URL and HA_TOKEN needed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	reg, err := haFetchRegistry(ctx, haTarget{base: strings.TrimRight(u, "/"), token: tok})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d devices, %d registry entities, %d config entries, %d states", len(reg.devices), len(reg.entities), len(reg.entries), len(reg.states))
	for k, i := range haRegistryCounts(reg) {
		if k < 10 {
			t.Logf("%s: %d devices (%d nameless), %d loaded, ratio %.1f", i.Domain, i.Devices, i.Nameless, i.Loaded, i.ratio())
		}
	}
	t.Log(haRegistryResult(reg, Config{}).Msg)
}

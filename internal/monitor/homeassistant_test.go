package monitor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KrX3D/Homewarden/internal/store"
)

func TestHAResult(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	ent := func(id, state string, ago time.Duration) haEntity {
		e := haEntity{ID: id, State: state, LastChanged: now.Add(-ago)}
		return e
	}
	states := []haEntity{
		ent("sensor.bitshake_power", "unavailable", 8*24*time.Hour),
		ent("sensor.bitshake_energy", "unavailable", 8*24*time.Hour),
		ent("sensor.plug_power", "unavailable", 5*time.Minute),
		ent("sensor.door", "unknown", 3*time.Hour),
		ent("light.kitchen", "on", time.Hour),
	}
	states[0].Attributes.Name = "Smart meter power"
	cases := []struct {
		name string
		cfg  Config
		ok   bool
		want string
	}{
		{"long unavailable", Config{Entities: []string{"sensor.bitshake_power"}}, false, "Smart meter power (sensor.bitshake_power) unavailable for 8 d"},
		{"wildcard", Config{Entities: []string{"sensor.bitshake_*"}}, false, "2 unavailable"},
		{"short outage", Config{Entities: []string{"sensor.plug_power", "light.kitchen"}}, true, "1 of 2 available, 1 briefly"},
		{"short outage, low limit", Config{Entities: []string{"sensor.plug_power"}, UnavailableMinutes: 5}, false, "unavailable for 5 min"},
		{"unknown ignored", Config{Entities: []string{"sensor.door"}}, true, "1 available"},
		{"unknown counted", Config{Entities: []string{"sensor.door"}, CountUnknown: true}, false, "unknown for 3 h"},
		{"missing", Config{Entities: []string{"light.kitchen", "sensor.gone"}}, false, "not found: sensor.gone"},
		{"case and spaces", Config{Entities: []string{" Light.Kitchen "}}, true, "1 available"},
		{"empty", Config{}, false, "no entities"},
	}
	for _, tc := range cases {
		r := haResult(states, tc.cfg, now)
		if r.OK != tc.ok || !strings.Contains(r.Msg, tc.want) {
			t.Errorf("%s: got %v %q", tc.name, r.OK, r.Msg)
		}
	}
}

func TestHATargetFromDevice(t *testing.T) {
	creds := func(_ context.Context, id int64) (string, string, error) { return "", "tok", nil }
	d := store.Device{Host: "192.168.90.50", Config: json.RawMessage(`{"settings":{"token":7,"https":true,"port":8443}}`)}
	tg, err := haTargetFromDevice(context.Background(), d, creds)
	if err != nil || tg.base != "https://192.168.90.50:8443" || tg.token != "tok" {
		t.Fatalf("got %+v %v", tg, err)
	}
	d.Config = json.RawMessage(`{"settings":{"token":7,"url":"http://ha.lan:8123/"}}`)
	if tg, _ = haTargetFromDevice(context.Background(), d, creds); tg.base != "http://ha.lan:8123" {
		t.Fatalf("url: %q", tg.base)
	}
	d.Config = json.RawMessage(`{"settings":{}}`)
	if _, err = haTargetFromDevice(context.Background(), d, creds); err == nil {
		t.Fatal("want error without token")
	}
}

// TestHACheckFake runs the whole check against a fake /api/states.
func TestHACheckFake(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/states" || r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Date", "Sat, 14 Nov 2026 12:00:00 GMT")
		w.Write([]byte(`[{"entity_id":"sensor.bitshake_power","state":"unavailable","last_changed":"2026-11-06T09:00:00.000000+00:00","attributes":{}}]`))
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
	res := r.Test(ctx, "ha", Config{Entities: []string{"sensor.bitshake_power"}}, &d.ID)
	if res.OK || !strings.Contains(res.Msg, "unavailable for 8 d") {
		t.Fatalf("got %v %q", res.OK, res.Msg)
	}
	r.Creds = func(context.Context, int64) (string, string, error) { return "", "wrong", nil }
	if res = r.Test(ctx, "ha", Config{Entities: []string{"x"}}, &d.ID); res.OK || !strings.Contains(res.Msg, "refused") {
		t.Fatalf("bad token: %v %q", res.OK, res.Msg)
	}
}

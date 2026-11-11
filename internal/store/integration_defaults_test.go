package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestWithDefaults(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetIntegrationDefaults(ctx, "octoprint", IntegrationDefaults{
		Settings:   map[string]any{"apiKey": float64(7), "port": float64(80), "insecure": true},
		Connection: map[string]any{"credentialId": float64(3), "user": "pi", "port": float64(22), "sudo": true},
	}); err != nil {
		t.Fatal(err)
	}
	read := func(d Device) (map[string]any, map[string]any) {
		var cfg struct {
			Settings   map[string]any `json:"settings"`
			Connection map[string]any `json:"connection"`
		}
		_ = json.Unmarshal(st.WithDefaults(ctx, d).Config, &cfg)
		return cfg.Settings, cfg.Connection
	}

	// Own value wins, empty value and missing keys take the default.
	set, c := read(Device{Kind: "octoprint", Config: json.RawMessage(`{"settings":{"port":5000,"apiKey":""}}`)})
	if set["port"] != float64(5000) || set["apiKey"] != float64(7) || set["insecure"] != true {
		t.Fatalf("settings: %v", set)
	}
	if c["credentialId"] != float64(3) || c["sudo"] != true || c["method"] != "ssh" {
		t.Fatalf("connection: %v", c)
	}

	// Own credential and agent keep their connection.
	_, c = read(Device{Kind: "octoprint", Config: json.RawMessage(`{"connection":{"method":"ssh","credentialId":9}}`)})
	if c["credentialId"] != float64(9) {
		t.Fatalf("own credential replaced: %v", c)
	}
	_, c = read(Device{Kind: "octoprint", Config: json.RawMessage(`{"connection":{"method":"agent"}}`)})
	if c["method"] != "agent" || c["credentialId"] != nil {
		t.Fatalf("agent replaced: %v", c)
	}

	// A credential in an integration setup counts as in use.
	if n, err := st.CredentialUsage(ctx, 3); err != nil || n != 1 {
		t.Fatalf("usage = %d, %v", n, err)
	}

	// Other kinds are untouched.
	set, _ = read(Device{Kind: "docker", Config: json.RawMessage(`{}`)})
	if len(set) != 0 {
		t.Fatalf("docker got defaults: %v", set)
	}
}

func TestNetworkInfo(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	udm, _ := st.CreateDevice(ctx, Device{Name: "UDM SE"})
	pi, _ := st.CreateDevice(ctx, Device{Name: "octopi"})
	if err := st.SetNetworkInfo(ctx, pi.ID, udm.ID, json.RawMessage(`{"via":"wired","port":5}`)); err != nil {
		t.Fatal(err)
	}
	_ = st.SetNetworkInfo(ctx, pi.ID, udm.ID, json.RawMessage(`{"via":"wired","port":6}`))
	got, err := st.NetworkInfoFor(ctx, pi.ID)
	if err != nil || len(got) != 1 || got[0].SourceName != "UDM SE" || string(got[0].Data) != `{"via":"wired","port":6}` {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestCredentialUsageCountsSettings(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.CredentialKeys = func() []string { return []string{"apiToken", "apiKey"} }
	_ = st.SetIntegrationDefaults(ctx, "proxmox", IntegrationDefaults{Settings: map[string]any{"apiToken": float64(2)}})
	st.CreateDevice(ctx, Device{Name: "a", Config: json.RawMessage(`{"settings":{"apiKey":7}}`)})
	st.CreateDevice(ctx, Device{Name: "b", Config: json.RawMessage(`{"settings":{"port":2}}`)})
	if n, _ := st.CredentialUsage(ctx, 2); n != 1 {
		t.Fatalf("token in integration setup: %d", n)
	}
	if n, _ := st.CredentialUsage(ctx, 7); n != 1 {
		t.Fatalf("token in device settings: %d", n)
	}
}

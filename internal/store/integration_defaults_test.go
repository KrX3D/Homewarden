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

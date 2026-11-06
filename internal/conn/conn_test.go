package conn

import (
	"encoding/json"
	"testing"

	"github.com/KrX3D/Homewarden/internal/store"
)

func TestSettingsRoundTrip(t *testing.T) {
	d := store.Device{Config: json.RawMessage(`{"other":{"a":1}}`)}
	if FromDevice(d).Port != 22 {
		t.Fatal("default port must be 22")
	}
	cfg, err := WithSettings(d, Settings{Method: "ssh", Port: 2222, CredentialID: 7, Sudo: true})
	if err != nil {
		t.Fatal(err)
	}
	d.Config = cfg
	s := FromDevice(d)
	if s.Port != 2222 || s.CredentialID != 7 || !s.Sudo || s.Method != "ssh" {
		t.Fatalf("round trip: %+v", s)
	}
	var m map[string]any
	json.Unmarshal(cfg, &m)
	if _, ok := m["other"]; !ok {
		t.Fatal("other config keys must survive")
	}
}

func TestParseFacts(t *testing.T) {
	f := ParseFacts("user=pi\nhostname=octopi\nos=Raspbian GNU/Linux 11 (bullseye)\nkernel=6.1.21-v8+\narch=aarch64\nsudo=yes\n")
	if f.User != "pi" || f.Hostname != "octopi" || !f.Sudo || f.Arch != "aarch64" || f.OS == "" {
		t.Fatalf("%+v", f)
	}
}

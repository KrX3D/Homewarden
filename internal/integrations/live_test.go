package integrations

// Read-only checks of the beta integrations against real systems. Skipped unless
// HW_LIVE=1. Only detect/discover/busy/metrics are called; refreshLists stays off,
// so nothing is changed on the target. Addresses and tokens come from the environment:
//
//	HW_LIVE=1 HA_URL=https://ha.example HA_TOKEN=... PVE_HOST=10.0.0.5 \
//	PVE_TOKEN_ID=viewer@pve!hw PVE_TOKEN_SECRET=... go test ./internal/integrations -run TestLive -v

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	builtin "github.com/KrX3D/Homewarden/integrations"
	"github.com/KrX3D/Homewarden/internal/store"
	"github.com/KrX3D/Homewarden/internal/vault"
)

func liveCall(t *testing.T, id, host, user, token string, settings map[string]any, fns ...string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	v, _ := vault.Load(dir)
	sec, _ := v.EncryptString(token)
	cred, err := st.CreateCredential(context.Background(), store.Credential{Name: "live", Type: store.CredToken, Username: user, Secret: sec})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"apiToken", "apiKey", "token"} {
		settings[k] = cred.ID
	}
	cfg, _ := json.Marshal(map[string]any{"settings": settings})
	in, err := NewRegistry(dir, st, builtin.FS).Install(context.Background(), BuiltinSource, id)
	if err != nil {
		t.Fatal(err)
	}
	h := &Host{St: st, Vault: v}
	for _, fn := range fns {
		var arg any
		if fn == "metrics" {
			arg = map[string]any{"smart": false}
		}
		out, err := h.Call(context.Background(), in, store.Device{ID: 1, Name: id, Host: host, Config: cfg}, fn, arg, func(l string) { t.Logf("[%s log] %s", fn, l) })
		if err != nil {
			t.Errorf("%s %s: %v", id, fn, err)
			continue
		}
		t.Logf("%s %s: %s", id, fn, out)
	}
}

func TestLiveHomeAssistant(t *testing.T) {
	tok, u := os.Getenv("HA_TOKEN"), os.Getenv("HA_URL")
	if os.Getenv("HW_LIVE") != "1" || tok == "" || u == "" {
		t.Skip("HW_LIVE=1, HA_URL and HA_TOKEN needed")
	}
	pu, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	liveCall(t, "home-assistant", pu.Hostname(), "", tok, map[string]any{"url": u}, "detect", "busy")
}

func TestLiveProxmox(t *testing.T) {
	tok, id, host := os.Getenv("PVE_TOKEN_SECRET"), os.Getenv("PVE_TOKEN_ID"), os.Getenv("PVE_HOST")
	if os.Getenv("HW_LIVE") != "1" || tok == "" || id == "" || host == "" {
		t.Skip("HW_LIVE=1, PVE_HOST, PVE_TOKEN_ID (user@realm!name) and PVE_TOKEN_SECRET needed")
	}
	liveCall(t, "proxmox", host, id, tok,
		map[string]any{"insecure": true, "refreshLists": false, "includeLXC": true}, "detect", "discover", "busy", "metrics")
}

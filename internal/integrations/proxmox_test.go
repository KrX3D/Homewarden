package integrations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	builtin "github.com/KrX3D/Homewarden/integrations"
	"github.com/KrX3D/Homewarden/internal/store"
	"github.com/KrX3D/Homewarden/internal/vault"
)

// fakeAPI answers Proxmox VE / PBS paths with canned data and checks the token header.
func fakeAPI(t *testing.T, scheme string, routes map[string]any) *httptest.Server {
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), scheme+"=root@pam!hw") {
			w.WriteHeader(401)
			return
		}
		data, ok := routes[r.URL.Path]
		if !ok {
			t.Logf("unhandled %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
}

func runDetect(t *testing.T, id string, srv *httptest.Server, settings map[string]any) map[string]any {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	v, _ := vault.Load(dir)
	sec, _ := v.EncryptString("secret-uuid")
	cred, err := st.CreateCredential(context.Background(), store.Credential{Name: "tok", Type: store.CredToken, Username: "root@pam!hw", Secret: sec})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(srv.URL)
	settings["apiToken"] = cred.ID
	settings["port"] = u.Port()
	cfg, _ := json.Marshal(map[string]any{"settings": settings})
	r := NewRegistry(dir, st, builtin.FS)
	in, err := r.Install(context.Background(), BuiltinSource, id)
	if err != nil {
		t.Fatal(err)
	}
	h := &Host{St: st, Vault: v}
	out, err := h.Call(context.Background(), in, store.Device{ID: 1, Name: "x", Host: u.Hostname(), Config: cfg}, "detect", nil, func(l string) { t.Log(l) })
	if err != nil {
		t.Fatalf("%s detect: %v", id, err)
	}
	var res map[string]any
	json.Unmarshal(out, &res)
	return res
}

func TestProxmoxDetect(t *testing.T) {
	srv := fakeAPI(t, "PVEAPIToken", map[string]any{
		"/api2/json/nodes":                []map[string]any{{"node": "pve"}},
		"/api2/json/version":              map[string]any{"version": "8.2.4"},
		"/api2/json/nodes/pve/apt/update": []map[string]any{{"Package": "proxmox-kernel-6.8", "OldVersion": "6.8.8-2", "Version": "6.8.12-1", "Origin": "Proxmox"}, {"Package": "libssl3", "OldVersion": "3.0.11", "Version": "3.0.13", "Origin": "Debian-Security"}},
		"/api2/json/nodes/pve/status":     map[string]any{"kversion": "Linux 6.8.8-2-pve", "cpu": 0.05, "memory": map[string]any{"used": 4, "total": 16}, "uptime": 1000},
		"/api2/json/nodes/pve/lxc":        []map[string]any{{"vmid": 101, "name": "docker", "status": "running"}},
		"/api2/json/nodes/pve/qemu":       []map[string]any{{"vmid": 200, "name": "haos", "status": "stopped"}},
	})
	defer srv.Close()
	res := runDetect(t, "proxmox", srv, map[string]any{"includeLXC": false, "insecure": true})
	comps := res["components"].([]any)
	if len(comps) != 3 {
		t.Fatalf("want os + 2 packages, got %d", len(comps))
	}
	sec := comps[2].(map[string]any)
	if sec["security"] != true {
		t.Fatalf("Debian-Security package must be flagged: %v", sec)
	}
	facts := res["facts"].(map[string]any)
	if facts["guests"] != float64(2) || facts["memoryPercent"] != float64(25) {
		t.Fatalf("facts: %v", facts)
	}
}

func TestPBSDetect(t *testing.T) {
	srv := fakeAPI(t, "PBSAPIToken", map[string]any{
		"/api2/json/version":                        map[string]any{"version": "3.2", "release": "7"},
		"/api2/json/nodes/localhost/apt/update":     []map[string]any{},
		"/api2/json/status/datastore-usage":         []map[string]any{{"store": "main", "total": 1000, "used": 900}},
		"/api2/json/admin/datastore/main/snapshots": []map[string]any{{"backup-time": 1000}},
		"/api2/json/nodes/localhost/tasks":          []map[string]any{{"worker_type": "backup", "worker_id": "vm/100", "status": "error: timeout"}},
	})
	defer srv.Close()
	res := runDetect(t, "proxmox-backup", srv, map[string]any{"insecure": true})
	facts := res["facts"].(map[string]any)
	w, _ := json.Marshal(facts["warnings"])
	for _, want := range []string{"datastore_full", "backup_old", "backup_errors"} {
		if !strings.Contains(string(w), want) {
			t.Fatalf("missing warning %s in %s (facts %v)", want, w, facts)
		}
	}
}

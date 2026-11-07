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
	out, err := runFn(t, id, srv, settings, "detect")
	if err != nil {
		t.Fatalf("%s detect: %v", id, err)
	}
	var res map[string]any
	json.Unmarshal(out, &res)
	return res
}

// runFn installs the built-in integration and calls fn against srv.
func runFn(t *testing.T, id string, srv *httptest.Server, settings map[string]any, fn string, arg ...any) (json.RawMessage, error) {
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
	settings["apiKey"] = cred.ID
	settings["token"] = cred.ID
	settings["port"] = u.Port()
	cfg, _ := json.Marshal(map[string]any{"settings": settings})
	r := NewRegistry(dir, st, builtin.FS)
	in, err := r.Install(context.Background(), BuiltinSource, id)
	if err != nil {
		t.Fatal(err)
	}
	h := &Host{St: st, Vault: v}
	return h.Call(context.Background(), in, store.Device{ID: 1, Name: "x", Host: u.Hostname(), Config: cfg}, fn, firstArg(arg), func(l string) { t.Log(l) })
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
	}	// No SSH in this test: listed, but not installable, with a hint.
	if w, _ := json.Marshal(facts["warnings"]); !strings.Contains(string(w), "ssh_needed") {
		t.Fatalf("want ssh_needed warning, got %s", w)
	}
	kernel := comps[1].(map[string]any)
	if kernel["updatable"] != false || kernel["meta"].(map[string]any)["warning"] != "reboots" {
		t.Fatalf("kernel: %v", kernel)
	}
	if m := sec["meta"].(map[string]any); m["warning"] != nil {
		t.Fatalf("libssl3 restarts nothing Homewarden knows about: %v", m)
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

func TestUniFiDetect(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-KEY") != "secret-uuid" {
			w.WriteHeader(401)
			return
		}
		var data any
		switch r.URL.Path {
		case "/proxy/network/api/s/default/stat/sysinfo":
			data = []map[string]any{{"version": "9.0.114", "update_available": false}}
		case "/proxy/network/api/s/default/stat/device":
			data = []map[string]any{
				{"mac": "aa:bb", "name": "AP Living", "type": "uap", "model": "U6LR", "version": "6.6.55", "upgradable": true, "upgrade_to_firmware": "6.6.77", "state": 1},
				{"mac": "cc:dd", "name": "Switch", "type": "usw", "version": "7.0.50", "upgradable": false, "state": 0},
			}
		default:
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"meta": map[string]any{"rc": "ok"}, "data": data})
	}))
	defer srv.Close()
	// The fake checks X-API-KEY against the credential secret runDetect stores.
	res := runDetect(t, "unifi", srv, map[string]any{"insecure": true, "unifiOS": true, "site": "default"})
	comps := res["components"].([]any)
	if len(comps) != 3 {
		t.Fatalf("want app + 2 devices, got %v", comps)
	}
	ap := comps[1].(map[string]any)
	if ap["updateAvailable"] != true || ap["available"] != "6.6.77" {
		t.Fatalf("AP: %v", ap)
	}
	facts := res["facts"].(map[string]any)
	if facts["offlineDevices"] != float64(1) {
		t.Fatalf("facts: %v", facts)
	}
}

func TestUniFiDiscoverNetwork(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data any
		switch r.URL.Path {
		case "/proxy/network/api/s/default/stat/device":
			data = []map[string]any{{"mac": "sw:01", "name": "Switch Office", "type": "usw", "state": 1}, {"mac": "ap:01", "name": "AP Living", "type": "uap", "state": 1}}
		case "/proxy/network/api/s/default/stat/sta":
			data = []map[string]any{
				{"mac": "aa:01", "ip": "192.168.90.20", "name": "octopi", "is_wired": true, "sw_mac": "sw:01", "sw_port": 5, "network": "LAN", "uptime": 3600},
				{"mac": "aa:02", "ip": "192.168.20.7", "hostname": "phone", "is_wired": false, "ap_mac": "ap:01", "essid": "Home", "signal": -58, "tx_rate": 866000},
			}
		default:
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"meta": map[string]any{"rc": "ok"}, "data": data})
	}))
	defer srv.Close()
	out, err := runFn(t, "unifi", srv, map[string]any{"insecure": true, "unifiOS": true, "site": "default"}, "discover")
	if err != nil {
		t.Fatal(err)
	}
	var found []map[string]any
	json.Unmarshal(out, &found)
	if len(found) != 4 {
		t.Fatalf("want 2 devices + 2 clients, got %d", len(found))
	}
	wired, wifi := found[2], found[3]
	if wired["enrichOnly"] != true {
		t.Fatalf("clients must be enrich-only without importClients: %v", wired)
	}
	n := wired["network"].(map[string]any)
	if n["via"] != "wired" || n["uplink"] != "Switch Office" || n["port"] != float64(5) {
		t.Fatalf("wired: %v", n)
	}
	n = wifi["network"].(map[string]any)
	if n["via"] != "wifi" || n["uplink"] != "AP Living" || n["ssid"] != "Home" || n["signal"] != float64(-58) || n["txMbps"] != float64(866) {
		t.Fatalf("wifi: %v", n)
	}
}

func TestUniFiWrongDevice(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // nothing listens: like picking UniFi on a device that is not the console
	_, err := runFn(t, "unifi", srv, map[string]any{"insecure": true, "unifiOS": true, "site": "default"}, "detect")
	if err == nil || !strings.Contains(err.Error(), "only on the console") {
		t.Fatalf("want a hint about the console, got %v", err)
	}
}

func TestDeployDeckDetect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/login" {
			http.SetCookie(w, &http.Cookie{Name: "dd_session", Value: "abc"})
			w.Write([]byte(`{"ok":true}`))
			return
		}
		if c, err := r.Cookie("dd_session"); err != nil || c.Value != "abc" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/api/hosts":
			w.Write([]byte(`{"hosts":["PC1","LAPTOP"],"programs":[{"program":"7-Zip","script":"7ZipUpdate.ps1","localVersion":"24.08","onlineVersion":"24.09","installed":{"PC1":"24.08","LAPTOP":"23.01"},"installScriptId":9}]}`))
		case "/api/scripts":
			w.Write([]byte(`[{"id":3,"relPath":"7ZipUpdate.ps1","programName":"7-Zip","localVersion":"24.08","onlineVersion":"24.09"}]`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "t.db"))
	defer st.Close()
	v, _ := vault.Load(dir)
	sec, _ := v.EncryptString("pw")
	cred, _ := st.CreateCredential(context.Background(), store.Credential{Name: "dd", Type: store.CredPassword, Username: "admin", Secret: sec})
	u, _ := url.Parse(srv.URL)
	cfg, _ := json.Marshal(map[string]any{"settings": map[string]any{"login": cred.ID, "port": u.Port(), "perPC": true}})
	r := NewRegistry(dir, st, builtin.FS)
	in, err := r.Install(context.Background(), BuiltinSource, "deploydeck")
	if err != nil {
		t.Fatal(err)
	}
	out, err := (&Host{St: st, Vault: v}).Call(context.Background(), in, store.Device{ID: 1, Host: u.Hostname(), Config: cfg}, "detect", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Components []map[string]any `json:"components"`
	}
	json.Unmarshal(out, &res)
	if len(res.Components) != 2 || res.Components[0]["id"] != "prog:3" || res.Components[1]["id"] != "pc:LAPTOP:9" {
		t.Fatalf("want share update + LAPTOP behind, got %v", res.Components)
	}
}

func firstArg(a []any) any {
	if len(a) == 0 {
		return nil
	}
	return a[0]
}

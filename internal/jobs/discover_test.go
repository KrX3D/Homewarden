package jobs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	builtin "github.com/KrX3D/Homewarden/integrations"
	"github.com/KrX3D/Homewarden/internal/events"
	"github.com/KrX3D/Homewarden/internal/integrations"
	"github.com/KrX3D/Homewarden/internal/store"
	"github.com/KrX3D/Homewarden/internal/vault"
)

// A Proxmox LXC that a network scan already put in the inbox (with MAC and IP) must
// end up as one device: the Proxmox child, now with MAC and address.
func TestProxmoxGuestCombinesWithScanEntry(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	v, _ := vault.Load(dir)

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data := map[string]any{
			"/api2/json/nodes":                    []map[string]any{{"node": "pve"}},
			"/api2/json/nodes/pve/lxc":            []map[string]any{{"vmid": 103, "name": "nginxproxymanager", "status": "running"}, {"vmid": 104, "name": "adguard", "status": "running"}},
			"/api2/json/nodes/pve/qemu":           []map[string]any{{"vmid": 200, "name": "haos", "status": "running"}},
			"/api2/json/nodes/pve/lxc/103/config": map[string]any{"hostname": "nginxproxymanager", "net0": "name=eth0,bridge=vmbr0,hwaddr=BC:24:11:FF:77:5B,ip=dhcp,type=veth"},
			"/api2/json/nodes/pve/lxc/103/interfaces": []map[string]any{
				{"name": "lo", "inet": "127.0.0.1/8", "hwaddr": "00:00:00:00:00:00"},
				{"name": "eth0", "inet": "192.168.90.203/24", "hwaddr": "bc:24:11:ff:77:5b"},
			},
			"/api2/json/nodes/pve/lxc/104/config":  map[string]any{"net0": "name=eth0,bridge=vmbr0,hwaddr=BC:24:11:00:00:04,ip=192.168.90.53/24"},
			"/api2/json/nodes/pve/qemu/200/config": map[string]any{"net0": "virtio=BC:24:11:00:02:00,bridge=vmbr0"},
		}[r.URL.Path]
		if data == nil {
			w.WriteHeader(500) // e.g. no guest agent, missing interfaces endpoint
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)

	reg := integrations.NewRegistry(dir, st, builtin.FS)
	in, err := reg.Install(ctx, integrations.BuiltinSource, "proxmox")
	if err != nil {
		t.Fatal(err)
	}
	sec, _ := v.EncryptString("secret")
	cred, _ := st.CreateCredential(ctx, store.Credential{Name: "tok", Type: store.CredToken, Username: "root@pam!hw", Secret: sec})
	cfg, _ := json.Marshal(map[string]any{"settings": map[string]any{"apiToken": cred.ID, "port": u.Port(), "insecure": true}})
	pve, _ := st.CreateDevice(ctx, store.Device{Name: "Proxmox 2", Kind: "proxmox", Host: u.Hostname(), Config: cfg})

	// The user's situation: an earlier Proxmox discovery created the LXC without MAC
	// or address, and the network scan found the same machine with both.
	pid := pve.ID
	child, _ := st.CreateDevice(ctx, store.Device{Name: "nginxproxymanager", ParentID: &pid, Status: store.StatusDiscovered,
		Source: "proxmox:" + itoa(pve.ID), ExternalID: "lxc/103"})
	// What the network scan found earlier: same MAC, untouched in the inbox.
	scan, _ := st.CreateDevice(ctx, store.Device{Name: "Proxmox - nGinX", Host: "192.168.90.203", MAC: "bc:24:11:ff:77:5b",
		Status: store.StatusDiscovered, Source: "scan", ExternalID: "192.168.90.203"})
	// An added device for adguard (the user added the scan result earlier).
	added, _ := st.CreateDevice(ctx, store.Device{Name: "AdGuard", Host: "192.168.90.53", Source: "scan", ExternalID: "192.168.90.53"})

	br := events.New()
	sw := &Software{St: st, Reg: reg, Host: &integrations.Host{St: st, Vault: v}, Runner: New(st, br), Events: br}
	job, _ := st.CreateJob(ctx, store.Job{Type: "scan", Title: "t"})
	run := &Run{Job: job, runner: sw.Runner}
	for i := 0; i < 2; i++ { // the second run must not create anything new
		if _, err := sw.discover(ctx, run, in, pve); err != nil {
			t.Fatal(err)
		}
	}

	all, _ := st.Devices(ctx, "")
	byName := map[string]store.Device{}
	for _, d := range all {
		byName[d.Name] = d
	}
	if _, ok := byName["Proxmox - nGinX"]; ok {
		t.Fatal("the scan duplicate should be gone")
	}
	npm := byName["nginxproxymanager"]
	if npm.ID != child.ID || npm.MAC != "bc:24:11:ff:77:5b" || npm.Host != "192.168.90.203" || npm.ParentID == nil || *npm.ParentID != pve.ID {
		t.Fatalf("nginxproxymanager: %+v", npm)
	}
	if _, err := st.Device(ctx, scan.ID); err == nil {
		t.Fatal("scan entry still exists")
	}
	ag := byName["AdGuard"]
	if ag.ID != added.ID || ag.ParentID == nil || *ag.ParentID != pve.ID || ag.Source != "proxmox:"+itoa(pve.ID) {
		t.Fatalf("added AdGuard should be adopted as the child: %+v", ag)
	}
	if _, ok := byName["adguard"]; ok {
		t.Fatal("no second adguard entry expected")
	}
	if h := byName["haos"]; h.MAC != "bc:24:11:00:02:00" || h.Host != "" {
		t.Fatalf("haos (no guest agent): %+v", h)
	}
	if len(all) != 4 { // pve, nginxproxymanager, AdGuard, haos
		names := []string{}
		for _, d := range all {
			names = append(names, d.Name)
		}
		t.Fatalf("want 4 devices, got %v", names)
	}
}

func itoa(i int64) string { b, _ := json.Marshal(i); return string(b) }

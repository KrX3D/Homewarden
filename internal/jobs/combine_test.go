package jobs

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/KrX3D/Homewarden/internal/events"
	"github.com/KrX3D/Homewarden/internal/store"
)

// The same LXC in the inbox twice, from UniFi (client list, with the user's alias) and
// from Proxmox (guest list). Whichever integration scans, the Proxmox entry stays and
// takes the alias.
func TestCombineAcrossIntegrations(t *testing.T) {
	for _, observer := range []bool{false, true} {
		ctx := context.Background()
		st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
		if err != nil {
			t.Fatal(err)
		}
		udm, _ := st.CreateDevice(ctx, store.Device{Name: "UDM SE", Kind: "unifi"})
		pve, _ := st.CreateDevice(ctx, store.Device{Name: "Proxmox", Kind: "proxmox"})
		u, p := udm.ID, pve.ID
		fromUniFi, _ := st.CreateDevice(ctx, store.Device{Name: "Proxmox - nGinX", Host: "192.168.90.203", MAC: "bc:24:11:ff:77:5b",
			ParentID: &u, Status: store.StatusDiscovered, Source: "unifi:" + itoa(u), ExternalID: "client:bc:24:11:ff:77:5b"})
		fromPVE, _ := st.CreateDevice(ctx, store.Device{Name: "nginxproxymanager", Host: "192.168.90.203", MAC: "bc:24:11:ff:77:5b",
			ParentID: &p, Status: store.StatusDiscovered, Source: "proxmox:" + itoa(p), ExternalID: "lxc/103"})
		br := events.New()
		sw := &Software{St: st, Events: br}

		var got store.Device
		if observer { // UniFi scan: the UniFi entry is the child, it carries network details
			f := discovered{ExternalID: "client:bc:24:11:ff:77:5b", MAC: "bc:24:11:ff:77:5b", Host: "192.168.90.203", Network: json.RawMessage(`{"via":"wired"}`)}
			got = sw.combine(ctx, fromUniFi, udm, "unifi:"+itoa(u), f)
		} else {
			f := discovered{ExternalID: "lxc/103", MAC: "bc:24:11:ff:77:5b", Host: "192.168.90.203"}
			got = sw.combine(ctx, fromPVE, pve, "proxmox:"+itoa(p), f)
		}
		if got.ID != fromPVE.ID {
			t.Fatalf("observer=%v: kept %d, want the Proxmox entry %d", observer, got.ID, fromPVE.ID)
		}
		if _, err := st.Device(ctx, fromUniFi.ID); err == nil {
			t.Fatalf("observer=%v: UniFi copy still there", observer)
		}
		d, _ := st.Device(ctx, fromPVE.ID)
		if d.Name != "Proxmox - nGinX" {
			t.Fatalf("observer=%v: alias not kept, name %q", observer, d.Name)
		}
		st.Close()
	}
}

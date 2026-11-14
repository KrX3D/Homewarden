package integrations

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	builtin "github.com/KrX3D/Homewarden/integrations"
	"github.com/KrX3D/Homewarden/internal/store"
)

// Output of DRIFT_SH as Unraid prints it, with one finding of every kind.
const unraidDriftRaw = `@@flash
flashExport="e"
flashSecurity="public"
@@ident
NAME="KrX-NaS"
USE_NETBIOS="yes"
USE_UPNP="yes"
@@user
mounted
@@shares
appdata|1|e|public
Daten|1|e|private
isofiles|1|-|public
testA54hj5luZtlorr494|0|e|public
@@autostart
Gitea
AdGuard-Home 10
@@docker
/Gitea|gitea/gitea|0|bind=/mnt/user/appdata/gitea;
/AdGuard-Home|adguard/adguardhome:latest|1|bind=/mnt/user/appdata/adguard;
/postgresql17|postgres:17|1|bind=/mnt/user/appdata/postgres;
/homepage|ghcr.io/gethomepage/homepage:latest|0|bind=/var/run/docker.sock;
/netbootxyz|netbootxyz/netbootxyz@sha256:abc|0|volume=/var/lib/docker/volumes/x;
/emqx|192.168.90.70:3000/krx/emqx|1|volume=/var/lib/docker/volumes/emqx;
`

func runUnraidDrift(t *testing.T, raw, ignore string) map[string]any {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	r := NewRegistry(dir, st, builtin.FS)
	in, err := r.Install(context.Background(), BuiltinSource, "unraid")
	if err != nil {
		t.Fatal(err)
	}
	h := &Host{St: st}
	out, err := h.Call(context.Background(), in, store.Device{ID: 1, Name: "nas", Config: json.RawMessage(`{}`)}, "driftFindings",
		map[string]any{"raw": raw, "ignore": ignore}, func(l string) { t.Log(l) })
	if err != nil {
		t.Fatal(err)
	}
	var res map[string]any
	json.Unmarshal(out, &res)
	return res
}

func TestUnraidDrift(t *testing.T) {
	res := runUnraidDrift(t, unraidDriftRaw, "")
	want := map[string]any{
		"public_flash":    "flash",
		"public_shares":   "appdata, testA54hj5luZtlorr494",
		"smb1_on":         "NetBIOS",
		"upnp_on":         "UPnP",
		"autostart_off":   "emqx, homepage, netbootxyz, postgresql17",
		"no_healthcheck":  "Gitea, homepage, netbootxyz",
		"latest_tag":      "AdGuard-Home (adguard/adguardhome:latest), emqx (192.168.90.70:3000/krx/emqx), Gitea (gitea/gitea)",
		"stale_share_cfg": "testA54hj5luZtlorr494.cfg",
	}
	if !reflect.DeepEqual(res["details"], want) {
		t.Fatalf("details:\n got %v\nwant %v", res["details"], want)
	}
	if w := res["warnings"].([]any); len(w) != 8 || w[0] != "public_flash" || w[7] != "stale_share_cfg" {
		t.Fatalf("warnings: %v", w)
	}
}

func TestUnraidDriftIgnoreAndClean(t *testing.T) {
	res := runUnraidDrift(t, unraidDriftRaw, "flash:public, homepage, netbootxyz:autostart,\n emqx : autostart, POSTGRESQL17:autostart, testA54hj5luZtlorr494:cfg")
	d := res["details"].(map[string]any)
	if _, ok := d["public_flash"]; ok {
		t.Fatalf("flash:public ignored, got %v", d)
	}
	if d["autostart_off"] != nil || d["no_healthcheck"] != "Gitea, netbootxyz" || d["stale_share_cfg"] != nil || d["public_shares"] != "appdata, testA54hj5luZtlorr494" {
		t.Fatalf("details: %v", d)
	}
	// Fixed server: array stopped (no /mnt/user), everything private, nothing running.
	clean := "@@flash\nflashExport=\"e\"\nflashSecurity=\"private\"\n@@ident\nUSE_NETBIOS=\"no\"\nUSE_UPNP=\"no\"\n@@user\n@@shares\nold|0|e|private\n@@autostart\n@@docker\n"
	if w := runUnraidDrift(t, clean, "")["warnings"].([]any); len(w) != 0 {
		t.Fatalf("want no warnings, got %v", w)
	}
}

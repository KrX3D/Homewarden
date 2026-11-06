package integrations

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	builtin "github.com/KrX3D/Homewarden/integrations"
	"github.com/KrX3D/Homewarden/internal/store"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.10.0", "1.9.3", 1}, {"1.2", "1.2.0", -1}, {"v2.0.1", "2.0.1", 0}, {"3.0.11-1~deb12u1", "3.0.13-1~deb12u1", -1}, {"1.0.0", "1.0.0-rc1", -1},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("%s vs %s: got %d want %d", c.a, c.b, got, c.want)
		}
	}
}

// Every built-in integration must be listed in index.json, parse, and compile.
func TestBuiltinCatalog(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	r := NewRegistry(t.TempDir(), st, builtin.FS)
	entries, errs := r.Catalog(context.Background(), nil)
	if len(errs) > 0 {
		t.Fatalf("catalog errors: %+v", errs)
	}
	if len(entries) == 0 {
		t.Fatal("no built-in integrations")
	}
	for _, e := range entries {
		in, err := r.Install(context.Background(), BuiltinSource, e.ID)
		if err != nil {
			t.Fatalf("install %s: %v", e.ID, err)
		}
		if in.Version != e.Version {
			t.Fatalf("%s: version mismatch", e.ID)
		}
	}
	if err := r.Load(context.Background()); err != nil || len(r.All()) != len(entries) {
		t.Fatalf("reload from disk: %v, %d loaded", err, len(r.All()))
	}
}

// The sandbox enforces the HTTP allowlist and the SSH permission.
func TestSandboxPermissions(t *testing.T) {
	m := Manifest{ID: "t", Version: "1.0.0", Name: Text{"en": "t"}, Entry: "main.js", Files: []string{"main.js"}, Permissions: Permissions{HTTPHosts: []string{"*.example.org"}}}
	in, err := compile(m, map[string]string{"main.js": `
		function blocked(ctx) { try { ctx.http.request({url: 'http://evil.test/'}); return 'no'; } catch (e) { return 'blocked'; } }
		function nossh(ctx) { try { ctx.ssh.exec('id'); return 'no'; } catch (e) { return 'blocked'; } }
		function echo(ctx, a) { return {name: ctx.device.name, arg: a, cmp: ctx.util.compareVersions('1.10', '1.9')}; }
	`}, "test")
	if err != nil {
		t.Fatal(err)
	}
	h := &Host{}
	dev := store.Device{ID: 1, Name: "pi", Host: "10.0.0.2", Config: json.RawMessage(`{}`)}
	for _, fn := range []string{"blocked", "nossh"} {
		out, err := h.Call(context.Background(), in, dev, fn, nil, nil)
		if err != nil || string(out) != `"blocked"` {
			t.Fatalf("%s: %s %v", fn, out, err)
		}
	}
	out, err := h.Call(context.Background(), in, dev, "echo", []string{"x"}, nil)
	if err != nil || string(out) != `{"arg":["x"],"cmp":1,"name":"pi"}` {
		t.Fatalf("echo: %s %v", out, err)
	}
}

package monitor

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/KrX3D/Homewarden/internal/store"
	"golang.org/x/net/dns/dnsmessage"
)

func TestParseResolvers(t *testing.T) {
	cases := []struct{ out, want string }{
		{"# generated\nsearch krx3d.de\nnameserver 1.1.1.1\nnameserver 192.168.90.53\nnameserver 1.1.1.1\n---\n", "1.1.1.1 192.168.90.53"},
		{"nameserver 127.0.0.53\noptions edns0\n---\nnameserver 192.168.90.53\nnameserver 192.168.90.1\n", "192.168.90.53 192.168.90.1"},
		{"nameserver 127.0.0.53\n---\n", "127.0.0.53"},
		{"192.168.90.53\r\n1.1.1.1\r\nfe80::1%12\r\n", "192.168.90.53 1.1.1.1 fe80::1"},
		{"nameserver not-an-ip\n", ""},
	}
	for _, c := range cases {
		if got := strings.Join(ParseResolvers(c.out), " "); got != c.want {
			t.Errorf("%q: got %q, want %q", c.out, got, c.want)
		}
	}
}

func fakeLookup(zones map[string]map[string]string) lookupFunc {
	return func(_ context.Context, server, name string) ([]string, error) {
		z, ok := zones[server]
		if !ok {
			return nil, &net.DNSError{Err: "i/o timeout", Name: name, Server: server, IsTimeout: true}
		}
		a, ok := z[name]
		if !ok {
			return nil, &net.DNSError{Err: "no such host", Name: name, Server: server, IsNotFound: true}
		}
		return strings.Fields(a), nil
	}
}

func TestCheckResolvers(t *testing.T) {
	adguard := map[string]string{"pve.krx3d.de": "192.168.90.10", "gitea.krx3d.de": "192.168.90.70"}
	zones := map[string]map[string]string{
		"192.168.90.53": adguard,
		"192.168.90.54": adguard,
		"1.1.1.1":       {},
		"8.8.8.8":       {"pve.krx3d.de": "85.1.2.3", "gitea.krx3d.de": "192.168.90.70"},
	}
	names := []string{"pve.krx3d.de", "gitea.krx3d.de"}
	cases := []struct {
		servers []string
		ok      bool
		want    string
	}{
		{[]string{"192.168.90.53", "192.168.90.54"}, true, "2 names on 192.168.90.53, 192.168.90.54"},
		{[]string{"1.1.1.1", "192.168.90.53"}, false, "1.1.1.1 does not know pve.krx3d.de, gitea.krx3d.de"},
		{[]string{"192.168.90.53", "8.8.8.8"}, false, "pve.krx3d.de differs: 192.168.90.53 = 192.168.90.10, 8.8.8.8 = 85.1.2.3"},
		{[]string{"192.168.90.53", "10.9.9.9"}, true, "no answer from 10.9.9.9 (from Homewarden)"},
		{[]string{"10.9.9.9"}, false, "no server could be asked"},
		{[]string{"127.0.0.11", "192.168.90.53"}, true, "skipped local 127.0.0.11"},
	}
	for _, c := range cases {
		r := checkResolvers(context.Background(), c.servers, names, fakeLookup(zones))
		if r.OK != c.ok || !strings.Contains(r.Msg, c.want) {
			t.Errorf("%v: got %v %q, want %v %q", c.servers, r.OK, r.Msg, c.ok, c.want)
		}
	}
	if r := checkResolvers(context.Background(), []string{"192.168.90.53"}, nil, fakeLookup(zones)); r.OK || r.Msg != "no names set" {
		t.Errorf("no names: %+v", r)
	}
}

func TestLookupProblem(t *testing.T) {
	if lookupProblem(errors.New("dial: refused")) != "unreachable" {
		t.Error("plain error")
	}
	if lookupProblem(&net.DNSError{Err: "server misbehaving"}) != "missing" {
		t.Error("servfail")
	}
}

// fakeDNS answers A queries on a local UDP port: known names get an address,
// "broken." gets SERVFAIL, everything else NXDOMAIN.
func fakeDNS(t *testing.T, known map[string]string) string {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var p dnsmessage.Parser
			h, err := p.Start(buf[:n])
			if err != nil {
				continue
			}
			q, err := p.Question()
			if err != nil {
				continue
			}
			rh := dnsmessage.Header{ID: h.ID, Response: true, RecursionAvailable: true}
			ip, ok := known[q.Name.String()]
			switch {
			case q.Name.String() == "broken.":
				rh.RCode = dnsmessage.RCodeServerFailure
			case !ok:
				rh.RCode = dnsmessage.RCodeNameError
			}
			b := dnsmessage.NewBuilder(nil, rh)
			b.EnableCompression()
			_ = b.StartQuestions()
			_ = b.Question(q)
			_ = b.StartAnswers()
			if ok && q.Type == dnsmessage.TypeA {
				var a [4]byte
				copy(a[:], net.ParseIP(ip).To4())
				_ = b.AResource(dnsmessage.ResourceHeader{Name: q.Name, Class: dnsmessage.ClassINET, TTL: 60}, dnsmessage.AResource{A: a})
			}
			out, _ := b.Finish()
			_, _ = pc.WriteTo(out, addr)
		}
	}()
	return pc.LocalAddr().String()
}

func TestDNSLookupReal(t *testing.T) {
	srv := fakeDNS(t, map[string]string{"pve.krx3d.de.": "192.168.90.10"})
	ctx := context.Background()
	ips, err := dnsLookup(ctx, srv, "pve.krx3d.de")
	if err != nil || strings.Join(ips, " ") != "192.168.90.10" {
		t.Fatalf("known: %v %v", ips, err)
	}
	if _, err := dnsLookup(ctx, srv, "nas.krx3d.de"); lookupProblem(err) != "missing" {
		t.Errorf("nxdomain: %v -> %s", err, lookupProblem(err))
	}
	if _, err := dnsLookup(ctx, srv, "broken"); lookupProblem(err) != "missing" {
		t.Errorf("servfail: %v -> %s", err, lookupProblem(err))
	}
	// Nothing listens here: unreachable, not missing.
	dead, _ := net.ListenPacket("udp", "127.0.0.1:0")
	addr := dead.LocalAddr().String()
	dead.Close()
	if _, err := dnsLookup(ctx, addr, "pve.krx3d.de"); lookupProblem(err) != "unreachable" {
		t.Errorf("dead server: %v -> %s", err, lookupProblem(err))
	}
}

func TestResolversFromDevice(t *testing.T) {
	r, _ := newRunner(t)
	ctx := context.Background()
	linux, _ := r.St.CreateDevice(ctx, store.Device{Name: "HA host", Kind: "linux-apt"})
	win, _ := r.St.CreateDevice(ctx, store.Device{Name: "PC", Kind: "windows"})
	var ran string
	r.Exec = func(_ context.Context, id int64, cmd string, _ time.Duration) (string, error) {
		ran = cmd
		if id == win.ID {
			return "192.168.90.53\r\n", nil
		}
		return "nameserver 1.1.1.1\nnameserver 192.168.90.53\n---\n", nil
	}
	r.lookup = fakeLookup(map[string]map[string]string{"1.1.1.1": {}, "192.168.90.53": {"pve.krx3d.de": "192.168.90.10"}})
	cfg := Config{Names: []string{"pve.krx3d.de"}}
	if res := r.Test(ctx, "resolvers", cfg, &linux.ID); res.OK || !strings.Contains(res.Msg, "1.1.1.1 does not know pve.krx3d.de") || ran != ResolverScript {
		t.Errorf("linux: %+v", res)
	}
	if res := r.Test(ctx, "resolvers", cfg, &win.ID); !res.OK || ran != ResolverScriptWindows {
		t.Errorf("windows: %+v", res)
	}
	// Servers set by hand: the device is not asked.
	ran = ""
	cfg.Servers = []string{"192.168.90.53"}
	if res := r.Test(ctx, "resolvers", cfg, nil); !res.OK || ran != "" {
		t.Errorf("manual: %+v", res)
	}
}

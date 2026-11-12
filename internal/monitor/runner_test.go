package monitor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KrX3D/Homewarden/internal/events"
	"github.com/KrX3D/Homewarden/internal/store"
)

func newRunner(t *testing.T) (*Runner, chan events.Event) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	br := events.New()
	ch := br.Subscribe()
	return NewRunner(st, br), ch
}

func addMonitor(t *testing.T, r *Runner, m store.Monitor, cfg Config) store.Monitor {
	t.Helper()
	m.Type, m.Enabled, m.Interval = "tcp", true, 60
	m.Config, _ = json.Marshal(cfg)
	out, err := r.St.SaveMonitor(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// record stores one result and returns the monitor afterwards.
func record(t *testing.T, r *Runner, id int64, ok bool) store.Monitor {
	t.Helper()
	ctx := context.Background()
	m, _ := r.St.Monitor(ctx, id)
	r.Record(ctx, m, nil, Result{OK: ok, Msg: "x"})
	m, _ = r.St.Monitor(ctx, id)
	return m
}

func states(ch chan events.Event) (out []string) {
	for {
		select {
		case ev := <-ch:
			if ev.Type == "monitor.state" {
				d := ev.Data.(map[string]any)
				s := d["status"].(string)
				if d["repeat"] == true {
					s += "+repeat"
				}
				out = append(out, s)
			}
		default:
			return out
		}
	}
}

func TestRetriesAndResend(t *testing.T) {
	r, ch := newRunner(t)
	m := addMonitor(t, r, store.Monitor{Name: "a"}, Config{Retries: 3, ResendEvery: 2})
	for i, want := range []string{"unknown", "unknown", "down", "down", "down", "down"} {
		if got := record(t, r, m.ID, false).Status; got != want {
			t.Fatalf("fail %d: %s, want %s", i+1, got, want)
		}
	}
	// down after 3 failures, repeated after 5 (3+2), not after 4 or 6.
	if got := strings.Join(states(ch), ","); got != "down,down+repeat" {
		t.Fatalf("events: %s", got)
	}
	if got := record(t, r, m.ID, true).Status; got != "up" {
		t.Fatalf("recovery: %s", got)
	}
}

func TestDependency(t *testing.T) {
	r, ch := newRunner(t)
	ctx := context.Background()
	parent := addMonitor(t, r, store.Monitor{Name: "NAS"}, Config{})
	child := addMonitor(t, r, store.Monitor{Name: "app", ParentID: &parent.ID}, Config{})

	// Parent has started failing: a child that would go down waits as "dependency".
	record(t, r, parent.ID, false)
	record(t, r, child.ID, false)
	if got := record(t, r, child.ID, false).Status; got != "dependency" {
		t.Fatalf("child while parent failing: %s", got)
	}
	if got := record(t, r, parent.ID, false).Status; got != "down" {
		t.Fatalf("parent: %s", got)
	}

	// With the parent down the child is not checked and stores no result.
	p, _ := r.St.Monitor(ctx, parent.ID)
	c, _ := r.St.Monitor(ctx, child.ID)
	r.mu.Lock()
	r.state[child.ID].fails = 0
	r.mu.Unlock()
	before, _ := r.St.MonitorResults(ctx, child.ID, 0)
	r.check(ctx, c, r.state[child.ID], &p)
	after, _ := r.St.MonitorResults(ctx, child.ID, 0)
	if len(after) != len(before) {
		t.Fatalf("dependency stored a result")
	}
	if got := strings.Join(states(ch), ","); got != "dependency,down" {
		t.Fatalf("events: %s", got)
	}

	// Parent back: the child's next good check makes it up again.
	record(t, r, parent.ID, true)
	if got := record(t, r, child.ID, true).Status; got != "up" {
		t.Fatalf("child after parent recovered: %s", got)
	}
}

func TestUpsideDown(t *testing.T) {
	r, _ := newRunner(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	if res := r.Test(context.Background(), "http", Config{URL: srv.URL, UpsideDown: true}, nil); res.OK {
		t.Fatal("reachable service counted as up in upside-down mode")
	}
	if res := r.Test(context.Background(), "http", Config{URL: "http://127.0.0.1:1/", UpsideDown: true, Timeout: 2}, nil); !res.OK {
		t.Fatalf("unreachable service counted as down in upside-down mode: %s", res.Msg)
	}
}

func TestContainerCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/containers/web/json":
			w.Write([]byte(`{"State":{"Status":"running","Health":{"Status":"healthy"}}}`))
		case "/containers/sick/json":
			w.Write([]byte(`{"State":{"Status":"running","Health":{"Status":"unhealthy"}}}`))
		case "/containers/plain/json":
			w.Write([]byte(`{"State":{"Status":"running"}}`))
		case "/containers/gone/json":
			w.Write([]byte(`{"State":{"Status":"exited","ExitCode":137}}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	ep := "tcp://" + strings.TrimPrefix(srv.URL, "http://")
	for name, want := range map[string]bool{"web": true, "plain": true, "sick": false, "gone": false, "missing": false} {
		res := Check(context.Background(), "container", Config{Endpoint: ep, Container: name})
		if res.OK != want {
			t.Errorf("%s: ok=%v (%s)", name, res.OK, res.Msg)
		}
	}
}

func TestContainerFromDevice(t *testing.T) {
	r, _ := newRunner(t)
	ctx := context.Background()
	host, _ := r.St.CreateDevice(ctx, store.Device{Name: "Unraid", Kind: "docker", Config: json.RawMessage(`{"settings":{"dockerEndpoint":"tcp://10.0.0.5:2375"}}`)})
	app, _ := r.St.CreateDevice(ctx, store.Device{Name: "Gitea", ParentID: &host.ID, ExternalID: "gitea"})
	cfg := Config{}
	r.resolve(ctx, "container", &cfg, &app.ID)
	if cfg.Endpoint != "tcp://10.0.0.5:2375" || cfg.Container != "gitea" {
		t.Fatalf("resolved %+v", cfg)
	}
}

func TestCertWarnLevel(t *testing.T) {
	now := time.Now()
	warn := Config{CertWarn: "7, 14,x"}.CertWarnDays()
	day := func(n int) int64 { return now.Add(time.Duration(n)*24*time.Hour - time.Hour).Unix() }
	for days, want := range map[int]int{30: 0, 14: 14, 10: 14, 7: 7, 2: 7, -1: 7} {
		if got := CertWarnLevel(warn, day(days), now); got != want {
			t.Errorf("%d days left: level %d, want %d", days, got, want)
		}
	}
}

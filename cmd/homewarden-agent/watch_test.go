package main

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KrX3D/Homewarden/internal/notify/channel"
)

func TestWatcherAlertsOnceAndRecovers(t *testing.T) {
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, r.Header.Get("Title")+"|"+string(b))
		mu.Unlock()
	}))
	defer srv.Close()
	ln, _ := net.Listen("tcp", "127.0.0.1:0") // the "NAS" answers
	defer ln.Close()

	cfg := config{Server: "http://hw.lan:8080"}
	path := filepath.Join(t.TempDir(), "agent.json")
	w := &watcher{cfgPath: path, cfg: &cfg, lastOK: time.Now()}
	w.update(&watchConfig{Type: "ntfy", Config: channel.Config{URL: srv.URL, Topic: "hw"}, Minutes: 5, Host: ln.Addr().String(),
		Texts: map[string]string{"downTitle": "down", "downBody": "{server} since {since}", "hostUp": "{host} up", "hostDown": "{host} down", "upTitle": "up", "upBody": "back"}})
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), `"topic": "hw"`) {
		t.Fatalf("watch config not saved: %s", b)
	}

	w.failed()
	if len(got) != 0 {
		t.Fatal("alerted before the limit")
	}
	w.failing = time.Now().Add(-6 * time.Minute)
	w.failed()
	w.failed()
	if len(got) != 1 || !strings.HasPrefix(got[0], "down|http://hw.lan:8080 since") || !strings.Contains(got[0], ln.Addr().String()+" up") {
		t.Fatalf("down alert: %q", got)
	}
	w.ok()
	w.ok()
	if len(got) != 2 || !strings.HasPrefix(got[1], "up|back") {
		t.Fatalf("recovery: %q", got)
	}
}

package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/KrX3D/Homewarden/internal/store"
	"github.com/KrX3D/Homewarden/internal/vault"
)

func TestMonitorTagsPickChannels(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	v, _ := vault.Load(dir)
	var mu sync.Mutex
	got := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		mu.Lock()
		got[r.URL.Path]++
		mu.Unlock()
	}))
	defer srv.Close()
	add := func(path string, tags []string) {
		b, _ := json.Marshal(Config{URL: srv.URL + path})
		enc, _ := v.EncryptString(string(b))
		if _, err := st.SaveNotifyChannel(ctx, store.NotifyChannel{Name: path, Type: "webhook", Config: enc, Events: []string{EvMonitors, EvJobs}, Tags: tags, Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	add("/all", nil)
	add("/critical", []string{"critical"})
	s := &Service{St: st, Vault: v}
	crit, _ := st.SaveMonitor(ctx, store.Monitor{Name: "NAS", Type: "ping", Enabled: true, Config: json.RawMessage(`{"tags":["critical"]}`)})
	plain, _ := st.SaveMonitor(ctx, store.Monitor{Name: "Printer", Type: "ping", Enabled: true, Config: json.RawMessage(`{}`)})
	s.onMonitor(ctx, crit.ID, "NAS", "down", "up", "x", nil, false)
	s.onMonitor(ctx, plain.ID, "Printer", "down", "up", "x", nil, false)
	s.Dispatch(ctx, EvJobs, Message{Title: "job failed"}) // other events ignore tags
	if got["/all"] != 3 || got["/critical"] != 2 {
		t.Fatalf("deliveries: %v", got)
	}
}

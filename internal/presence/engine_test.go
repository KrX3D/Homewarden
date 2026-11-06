package presence

import (
	"testing"
	"time"

	"github.com/KrX3D/Homewarden/internal/store"
)

func TestBootSequence(t *testing.T) {
	cfg := store.PresenceConfig{SettleSeconds: 60}
	tr := &tracker{}
	t0 := time.Unix(1_000_000, 0)
	state := store.StateUnknown

	step := func(reach, ready bool, at time.Duration, want string) {
		t.Helper()
		state = Next(state, tr, cfg, Input{Reached: reach, ReadyOK: ready, Now: t0.Add(at)})
		if state != want {
			t.Fatalf("at %v: got %s, want %s", at, state, want)
		}
	}

	step(false, false, 0, store.StateOffline)
	step(true, false, 30*time.Second, store.StateReachable) // pings, SSH not up yet
	step(true, true, 40*time.Second, store.StateReachable)  // SSH up, settling starts
	step(true, true, 90*time.Second, store.StateReachable)  // 50 s settled
	step(true, true, 101*time.Second, store.StateReady)     // 61 s settled
	step(false, false, 130*time.Second, store.StateReady)   // one lost ping: no flap
	step(false, false, 160*time.Second, store.StateOffline) // second miss: offline
}

func TestReadyDropResetsSettle(t *testing.T) {
	cfg := store.PresenceConfig{SettleSeconds: 30}
	tr := &tracker{}
	t0 := time.Unix(0, 0)
	s := Next(store.StateOffline, tr, cfg, Input{Reached: true, ReadyOK: true, Now: t0})
	s = Next(s, tr, cfg, Input{Reached: true, ReadyOK: false, Now: t0.Add(20 * time.Second)})
	s = Next(s, tr, cfg, Input{Reached: true, ReadyOK: true, Now: t0.Add(40 * time.Second)})
	if s != store.StateReachable {
		t.Fatalf("settle must restart after the service dropped, got %s", s)
	}
	s = Next(s, tr, cfg, Input{Reached: true, ReadyOK: true, Now: t0.Add(71 * time.Second)})
	if s != store.StateReady {
		t.Fatalf("want ready after a full settle, got %s", s)
	}
}

func TestNoSettle(t *testing.T) {
	tr := &tracker{}
	if s := Next(store.StateOffline, tr, store.PresenceConfig{}, Input{Reached: true, ReadyOK: true, Now: time.Now()}); s != store.StateReady {
		t.Fatalf("settle 0 means ready immediately, got %s", s)
	}
}

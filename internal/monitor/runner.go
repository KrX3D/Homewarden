package monitor

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/KrX3D/Homewarden/internal/events"
	"github.com/KrX3D/Homewarden/internal/store"
)

// failuresForDown is how many failed checks in a row mark a monitor down, so one lost
// packet does not page anyone.
const failuresForDown = 2

// Runner checks monitors on their intervals.
type Runner struct {
	St     *store.Store
	Events *events.Broker

	mu    sync.Mutex
	state map[int64]*track
	wake  chan struct{}
}

type track struct {
	next     time.Time
	fails    int
	running  bool
	lastPush time.Time
}

// NewRunner returns a runner.
func NewRunner(st *store.Store, br *events.Broker) *Runner {
	return &Runner{St: st, Events: br, state: map[int64]*track{}, wake: make(chan struct{}, 1)}
}

// Poke re-reads the monitors now (after edits).
func (r *Runner) Poke() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Run checks monitors until ctx ends.
func (r *Runner) Run(ctx context.Context) {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	prune := time.NewTicker(12 * time.Hour)
	defer prune.Stop()
	sem := make(chan struct{}, 16)
	for {
		r.round(ctx, sem)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-r.wake:
		case <-prune.C:
			_ = r.St.PruneMonitorResults(ctx, time.Now().AddDate(0, 0, -30).Unix())
		}
	}
}

func (r *Runner) round(ctx context.Context, sem chan struct{}) {
	mons, err := r.St.Monitors(ctx)
	if err != nil {
		return
	}
	now := time.Now()
	for _, m := range mons {
		if !m.Enabled {
			continue
		}
		r.mu.Lock()
		t := r.state[m.ID]
		if t == nil {
			t = &track{}
			r.state[m.ID] = t
		}
		due := !t.running && !now.Before(t.next)
		if due {
			t.running = true
			t.next = now.Add(time.Duration(max(m.Interval, 20)) * time.Second)
		}
		r.mu.Unlock()
		if !due {
			continue
		}
		sem <- struct{}{}
		go func(m store.Monitor, t *track) {
			defer func() { <-sem; r.mu.Lock(); t.running = false; r.mu.Unlock() }()
			r.check(ctx, m, t)
		}(m, t)
	}
}

func (r *Runner) check(ctx context.Context, m store.Monitor, t *track) {
	var res Result
	if m.Type == "push" {
		// A push monitor fails when no push arrived within 1.5 intervals.
		limit := time.Duration(float64(m.Interval)*1.5) * time.Second
		r.mu.Lock()
		last := t.lastPush
		r.mu.Unlock()
		if last.IsZero() && m.LastCheck != nil && m.Status == "up" {
			last = time.Unix(*m.LastCheck, 0) // after a restart
		}
		if last.IsZero() && m.LastCheck == nil {
			return // never pushed yet: stay unknown
		}
		if !last.IsZero() && time.Since(last) < limit {
			return
		}
		res = Result{Msg: "no push received"}
	} else {
		var cfg Config
		_ = json.Unmarshal(m.Config, &cfg)
		res = Check(ctx, m.Type, cfg)
	}
	r.Record(ctx, m, t, res)
}

// Record stores a result and publishes status changes. Push requests use it too.
func (r *Runner) Record(ctx context.Context, m store.Monitor, t *track, res Result) {
	if t == nil {
		r.mu.Lock()
		t = r.state[m.ID]
		if t == nil {
			t = &track{}
			r.state[m.ID] = t
		}
		r.mu.Unlock()
	}
	r.mu.Lock()
	if res.OK {
		t.fails = 0
		if m.Type == "push" {
			t.lastPush = time.Now()
		}
	} else {
		t.fails++
	}
	fails := t.fails
	r.mu.Unlock()
	status := m.Status
	switch {
	case res.OK:
		status = "up"
	case fails >= failuresForDown || m.Status == "unknown" && fails >= failuresForDown:
		status = "down"
	}
	var ms *float64
	if res.OK || res.Ms > 0 {
		v := res.Ms
		ms = &v
	}
	changed := status != m.Status
	if err := r.St.RecordMonitor(ctx, m.ID, res.OK, ms, res.Msg, status, changed, res.CertEnd); err != nil {
		log.Printf("monitor %s: %v", m.Name, err)
		return
	}
	r.Events.Publish("monitor.result", map[string]any{"id": m.ID, "ok": res.OK, "ms": ms, "status": status})
	if changed && status != "unknown" {
		r.Events.Publish("monitor.state", map[string]any{"id": m.ID, "name": m.Name, "status": status, "previous": m.Status, "msg": res.Msg, "deviceId": m.DeviceID})
	}
}

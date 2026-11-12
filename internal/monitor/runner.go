package monitor

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/KrX3D/Homewarden/internal/events"
	"github.com/KrX3D/Homewarden/internal/integrations"
	"github.com/KrX3D/Homewarden/internal/store"
)

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
	byID := map[int64]store.Monitor{}
	for _, m := range mons {
		byID[m.ID] = m
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
		var parent *store.Monitor
		if m.ParentID != nil {
			if p, ok := byID[*m.ParentID]; ok && p.Enabled {
				parent = &p
			}
		}
		sem <- struct{}{}
		go func(m store.Monitor, t *track) {
			defer func() { <-sem; r.mu.Lock(); t.running = false; r.mu.Unlock() }()
			r.check(ctx, m, t, parent)
		}(m, t)
	}
}

// parentDown: the parent is down, or itself waits on a down parent.
func parentDown(p *store.Monitor) bool {
	return p != nil && (p.Status == "down" || p.Status == "dependency")
}

func (r *Runner) check(ctx context.Context, m store.Monitor, t *track, parent *store.Monitor) {
	if parentDown(parent) {
		r.dependency(ctx, m, t, parent.Name)
		return
	}
	var cfg Config
	_ = json.Unmarshal(m.Config, &cfg)
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
		res = r.Test(ctx, m.Type, cfg, m.DeviceID)
	}
	r.Record(ctx, m, t, res)
}

// Test runs one check the way the runner does (container endpoint from the device,
// upside down applied), without storing anything.
func (r *Runner) Test(ctx context.Context, typ string, cfg Config, deviceID *int64) Result {
	r.resolve(ctx, typ, &cfg, deviceID)
	res := Check(ctx, typ, cfg)
	if cfg.UpsideDown {
		if res.OK {
			res.OK, res.Msg = false, "reachable: "+res.Msg
		} else {
			res.OK = true
		}
	}
	return res
}

// resolve fills a container monitor's endpoint and name from its device: a Docker host
// gives its endpoint, a container below a Docker host gives the host's endpoint and
// its own name.
func (r *Runner) resolve(ctx context.Context, typ string, cfg *Config, deviceID *int64) {
	if typ != "container" || deviceID == nil || cfg.Endpoint != "" && cfg.Container != "" {
		return
	}
	d, err := r.St.Device(ctx, *deviceID)
	if err != nil {
		return
	}
	host := d
	if d.Kind != "docker" && d.ParentID != nil {
		if p, err := r.St.Device(ctx, *d.ParentID); err == nil && p.Kind == "docker" {
			host = p
			if cfg.Container == "" {
				cfg.Container = d.ExternalID
			}
		}
	}
	if host.Kind == "docker" && cfg.Endpoint == "" {
		cfg.Endpoint, _ = integrations.Settings(r.St.WithDefaults(ctx, host))["dockerEndpoint"].(string)
	}
}

// dependency marks a monitor whose parent is down. The check does not run and no
// result is stored, so the outage counts only once, on the parent.
func (r *Runner) dependency(ctx context.Context, m store.Monitor, t *track, parent string) {
	r.mu.Lock()
	t.fails = 0
	r.mu.Unlock()
	if m.Status == "dependency" {
		return
	}
	msg := parent + " is down"
	if err := r.St.SetMonitorStatus(ctx, m.ID, "dependency", msg); err != nil {
		log.Printf("monitor %s: %v", m.Name, err)
		return
	}
	r.Events.Publish("monitor.state", map[string]any{"id": m.ID, "name": m.Name, "status": "dependency", "previous": m.Status, "msg": msg, "deviceId": m.DeviceID})
}

// parentFailing reports a parent that is down or has started failing, so a child that
// fails at the same time waits instead of sending its own alert.
func (r *Runner) parentFailing(ctx context.Context, m store.Monitor) bool {
	if m.ParentID == nil {
		return false
	}
	p, err := r.St.Monitor(ctx, *m.ParentID)
	if err != nil || !p.Enabled {
		return false
	}
	if parentDown(&p) {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	pt := r.state[p.ID]
	return pt != nil && pt.fails > 0
}

// Record stores a result and publishes status changes. Push requests use it too.
func (r *Runner) Record(ctx context.Context, m store.Monitor, t *track, res Result) {
	var cfg Config
	_ = json.Unmarshal(m.Config, &cfg)
	need := cfg.FailuresForDown()
	r.mu.Lock()
	if t == nil {
		t = r.state[m.ID]
		if t == nil {
			t = &track{}
			r.state[m.ID] = t
		}
	}
	if res.OK {
		t.fails = 0
		if m.Type == "push" {
			t.lastPush = time.Now()
		}
	} else {
		t.fails++
		if t.fails < need && cfg.RetryInterval > 0 {
			t.next = time.Now().Add(time.Duration(max(cfg.RetryInterval, 10)) * time.Second)
		}
	}
	fails := t.fails
	r.mu.Unlock()
	status := m.Status
	switch {
	case res.OK:
		status = "up"
	case fails >= need:
		status = "down"
	}
	if status == "down" && m.Status != "down" && r.parentFailing(ctx, m) {
		status = "dependency"
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
	// Still down: repeat the alert every ResendEvery failed checks.
	repeat := !changed && status == "down" && cfg.ResendEvery > 0 && fails > need && (fails-need)%cfg.ResendEvery == 0
	if changed && status != "unknown" || repeat {
		r.Events.Publish("monitor.state", map[string]any{"id": m.ID, "name": m.Name, "status": status, "previous": m.Status, "msg": res.Msg, "deviceId": m.DeviceID, "repeat": repeat})
	}
	if res.CertEnd != nil {
		if days := CertWarnLevel(cfg.CertWarnDays(), *res.CertEnd, time.Now()); days > 0 {
			r.Events.Publish("monitor.cert", map[string]any{"id": m.ID, "name": m.Name, "expires": *res.CertEnd, "level": days})
		}
	}
}

// CertWarnLevel returns the smallest warning threshold (days) the certificate is
// already within, 0 when none. warn is largest first.
func CertWarnLevel(warn []int, expires int64, now time.Time) int {
	left := time.Unix(expires, 0).Sub(now)
	level := 0
	for _, d := range warn {
		if left <= time.Duration(d)*24*time.Hour {
			level = d
		}
	}
	return level
}

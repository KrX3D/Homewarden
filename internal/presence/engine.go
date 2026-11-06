// Package presence decides whether devices are offline, booting or ready.
//
// A ping answer does not mean a device has finished booting. So presence has stages:
// offline -> reachable (ping/TCP answers) -> ready (the real service answers and has
// stayed up for the settle time). Scans only run once a device is ready.
package presence

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/KrX3D/Homewarden/internal/events"
	"github.com/KrX3D/Homewarden/internal/store"
)

// offlineAfter is how many failed reach checks in a row mark a device offline, so a
// single lost ping does not flap the state.
const offlineAfter = 2

const probeTimeout = 3 * time.Second

// tracker is the in-memory part of a device's state between checks.
type tracker struct {
	readySince time.Time
	fails      int
	next       time.Time
	running    bool
}

// Input is the result of one round of checks.
type Input struct {
	Reached bool
	ReadyOK bool
	Now     time.Time
}

// Next computes the next state. It is pure so the rules are easy to test.
func Next(cur string, t *tracker, cfg store.PresenceConfig, in Input) string {
	if !in.Reached {
		t.fails++
		t.readySince = time.Time{}
		if t.fails >= offlineAfter || cur == store.StateUnknown || cur == store.StateOffline {
			return store.StateOffline
		}
		return cur
	}
	t.fails = 0
	if !in.ReadyOK {
		t.readySince = time.Time{}
		return store.StateReachable
	}
	if cur == store.StateReady {
		return store.StateReady
	}
	if t.readySince.IsZero() {
		t.readySince = in.Now
	}
	if in.Now.Sub(t.readySince) >= time.Duration(cfg.SettleSeconds)*time.Second {
		return store.StateReady
	}
	return store.StateReachable
}

var errAgentOffline = errors.New("agent not connected")

// Engine runs presence checks for all active devices.
type Engine struct {
	// AgentOnline reports whether a device's agent polled recently (reach method "agent").
	AgentOnline func(deviceID int64) bool

	st     *store.Store
	events *events.Broker

	mu       sync.Mutex
	trackers map[int64]*tracker
	wake     chan struct{}

	readyMu sync.Mutex
	onReady []func(ctx context.Context, d store.Device)
}

// New returns an engine; call Run to start it.
func New(st *store.Store, br *events.Broker) *Engine {
	return &Engine{st: st, events: br, trackers: map[int64]*tracker{}, wake: make(chan struct{}, 1)}
}

// OnReady registers a callback for when a device becomes ready (e.g. start a scan).
func (e *Engine) OnReady(fn func(ctx context.Context, d store.Device)) {
	e.readyMu.Lock()
	e.onReady = append(e.onReady, fn)
	e.readyMu.Unlock()
}

// Poke makes the engine look at all devices now (after adds and edits).
func (e *Engine) Poke() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// Forget drops a device's in-memory state (after delete or config change).
func (e *Engine) Forget(id int64) {
	e.mu.Lock()
	delete(e.trackers, id)
	e.mu.Unlock()
}

// Run checks devices until ctx ends.
func (e *Engine) Run(ctx context.Context) {
	sem := make(chan struct{}, 16)
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	prune := time.NewTicker(6 * time.Hour)
	defer prune.Stop()
	for {
		e.round(ctx, sem)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-e.wake:
		case <-prune.C:
			// Presence history is kept for a year, like version history.
			_ = e.st.PrunePresence(ctx, time.Now().AddDate(-1, 0, 0).Unix())
		}
	}
}

func (e *Engine) round(ctx context.Context, sem chan struct{}) {
	devs, err := e.st.Devices(ctx, store.StatusActive)
	if err != nil {
		log.Printf("presence: %v", err)
		return
	}
	now := time.Now()
	for _, d := range devs {
		if d.Presence.Reach.Method == "none" || (d.Host == "" && d.Presence.Reach.Method != "agent") {
			continue
		}
		e.mu.Lock()
		t := e.trackers[d.ID]
		if t == nil {
			t = &tracker{}
			e.trackers[d.ID] = t
		}
		due := !t.running && !now.Before(t.next)
		if due {
			t.running = true
		}
		e.mu.Unlock()
		if !due {
			continue
		}
		sem <- struct{}{}
		go func(d store.Device, t *tracker) {
			defer func() { <-sem }()
			e.check(ctx, d, t)
		}(d, t)
	}
}

// CheckNow runs one check for a device immediately and returns its new state.
func (e *Engine) CheckNow(ctx context.Context, id int64) (store.Device, error) {
	d, err := e.st.Device(ctx, id)
	if err != nil {
		return d, err
	}
	e.mu.Lock()
	t := e.trackers[d.ID]
	if t == nil {
		t = &tracker{}
		e.trackers[d.ID] = t
	}
	t.running = true
	e.mu.Unlock()
	e.check(ctx, d, t)
	return e.st.Device(ctx, id)
}

func (e *Engine) check(ctx context.Context, d store.Device, t *tracker) {
	cfg := d.Presence
	var rtt time.Duration
	var err error
	if cfg.Reach.Method == "agent" {
		if e.AgentOnline == nil || !e.AgentOnline(d.ID) {
			err = errAgentOffline
		}
	} else {
		rtt, err = Run(ctx, d.Host, cfg.Reach, probeTimeout)
	}
	in := Input{Reached: err == nil, Now: time.Now()}
	if in.Reached {
		if cfg.Ready.Method == "none" || cfg.Ready.Method == "" {
			in.ReadyOK = true
		} else {
			_, rerr := Run(ctx, d.Host, cfg.Ready, probeTimeout)
			in.ReadyOK = rerr == nil
		}
	}

	e.mu.Lock()
	next := Next(d.State, t, cfg, in)
	// While settling, check again soon instead of waiting a full interval.
	interval := time.Duration(cfg.IntervalSeconds) * time.Second
	if next == store.StateReachable && in.ReadyOK && cfg.SettleSeconds > 0 {
		interval = min(interval, 10*time.Second)
	}
	t.next = in.Now.Add(interval)
	t.running = false
	e.mu.Unlock()

	at := in.Now.Unix()
	if in.Reached {
		var ms *float64
		if cfg.Reach.Method != "none" && cfg.Reach.Method != "agent" {
			v := float64(rtt.Microseconds()) / 1000
			ms = &v
		}
		_ = e.st.RecordSeen(ctx, d.ID, at, ms)
	}
	if next != d.State {
		if err := e.st.SetDeviceState(ctx, d.ID, next, at); err != nil {
			log.Printf("presence: save state of %s: %v", d.Name, err)
			return
		}
		e.events.Publish("device.state", map[string]any{"id": d.ID, "state": next, "since": at, "previous": d.State})
		if next == store.StateReady {
			d.State = next
			e.readyMu.Lock()
			cbs := append([]func(context.Context, store.Device){}, e.onReady...)
			e.readyMu.Unlock()
			for _, cb := range cbs {
				go cb(ctx, d)
			}
		}
	} else if in.Reached {
		e.events.Publish("device.seen", map[string]any{"id": d.ID, "at": at})
	}
}

// Package agent is the server side of the Homewarden agent: agents long-poll for
// commands over plain HTTP, so they work from any network that can reach Homewarden
// (other VLANs, behind NAT, through the reverse proxy) without opening ports.
package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// Command is one shell command for an agent.
type Command struct {
	ID      string `json:"id"`
	Cmd     string `json:"cmd"`
	Stdin   string `json:"stdin,omitempty"`
	Timeout int    `json:"timeout"` // seconds
}

// Result is what the agent reports back.
type Result struct {
	ID     string `json:"id"`
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
	Code   int    `json:"code"`
	Error  string `json:"error,omitempty"`
}

type queue struct {
	pending []Command
	notify  chan struct{}
}

// Hub routes commands to connected agents and results back to callers.
type Hub struct {
	mu      sync.Mutex
	queues  map[int64]*queue
	waiting map[string]chan Result
	seen    map[int64]time.Time
}

// NewHub returns an empty hub.
func NewHub() *Hub {
	return &Hub{queues: map[int64]*queue{}, waiting: map[string]chan Result{}, seen: map[int64]time.Time{}}
}

// OnlineWindow is how recent a poll must be for the agent to count as connected.
const OnlineWindow = 75 * time.Second

func (h *Hub) q(device int64) *queue {
	q, ok := h.queues[device]
	if !ok {
		q = &queue{notify: make(chan struct{}, 1)}
		h.queues[device] = q
	}
	return q
}

// Online reports whether the device's agent polled recently.
func (h *Hub) Online(device int64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	t, ok := h.seen[device]
	return ok && time.Since(t) < OnlineWindow
}

// ErrOffline means the agent is not connected.
var ErrOffline = errors.New("the agent is not connected")

func newID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Exec queues a command for a device's agent and waits for its result.
func (h *Hub) Exec(ctx context.Context, device int64, cmd, stdin string, timeout time.Duration) (Result, error) {
	if !h.Online(device) {
		return Result{}, ErrOffline
	}
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	c := Command{ID: newID(), Cmd: cmd, Stdin: stdin, Timeout: int(timeout.Seconds())}
	ch := make(chan Result, 1)
	h.mu.Lock()
	h.waiting[c.ID] = ch
	q := h.q(device)
	q.pending = append(q.pending, c)
	h.mu.Unlock()
	select {
	case q.notify <- struct{}{}:
	default:
	}
	defer func() {
		h.mu.Lock()
		delete(h.waiting, c.ID)
		h.mu.Unlock()
	}()
	// The agent enforces the timeout; allow for the poll round trip on top.
	t := time.NewTimer(timeout + 40*time.Second)
	defer t.Stop()
	select {
	case r := <-ch:
		if r.Error != "" {
			return r, errors.New(r.Error)
		}
		return r, nil
	case <-ctx.Done():
		return Result{Code: -1}, ctx.Err()
	case <-t.C:
		return Result{Code: -1}, errors.New("the agent did not answer in time")
	}
}

// Poll waits up to wait for commands for a device and records the heartbeat.
func (h *Hub) Poll(ctx context.Context, device int64, wait time.Duration) []Command {
	h.mu.Lock()
	h.seen[device] = time.Now()
	q := h.q(device)
	h.mu.Unlock()
	take := func() []Command {
		h.mu.Lock()
		defer h.mu.Unlock()
		out := q.pending
		q.pending = nil
		return out
	}
	if cmds := take(); len(cmds) > 0 {
		return cmds
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-q.notify:
	case <-t.C:
	case <-ctx.Done():
	}
	h.mu.Lock()
	h.seen[device] = time.Now()
	h.mu.Unlock()
	return take()
}

// Deliver hands a result to the waiting caller.
func (h *Hub) Deliver(r Result) {
	h.mu.Lock()
	ch, ok := h.waiting[r.ID]
	h.mu.Unlock()
	if ok {
		select {
		case ch <- r:
		default:
		}
	}
}

// Forget drops a device's state (after revoking its agent).
func (h *Hub) Forget(device int64) {
	h.mu.Lock()
	delete(h.queues, device)
	delete(h.seen, device)
	h.mu.Unlock()
}

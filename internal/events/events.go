// Package events fans server-side changes out to open browser tabs over
// Server-Sent Events, so device states update live without polling.
package events

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Event is one message; Type becomes the SSE event name.
type Event struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// Broker distributes events to subscribers.
type Broker struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

// New returns an empty broker.
func New() *Broker { return &Broker{subs: map[chan Event]struct{}{}} }

// Publish sends an event to every subscriber. Slow subscribers miss events rather than
// blocking the publisher; the UI reloads on reconnect anyway.
func (b *Broker) Publish(typ string, data any) {
	if b == nil {
		return
	}
	ev := Event{Type: typ, Data: data}
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// Subscribe returns a channel of all events for in-process consumers (MQTT).
// Call Unsubscribe when done.
func (b *Broker) Subscribe() chan Event { return b.subscribe() }

// Unsubscribe stops a channel from Subscribe.
func (b *Broker) Unsubscribe(ch chan Event) { b.unsubscribe(ch) }

func (b *Broker) subscribe() chan Event {
	ch := make(chan Event, 64)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *Broker) unsubscribe(ch chan Event) {
	b.mu.Lock()
	delete(b.subs, ch)
	b.mu.Unlock()
}

// ServeHTTP streams events until the client disconnects. Auth is applied by the caller.
func (b *Broker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	// Tells nginx not to buffer the stream.
	w.Header().Set("X-Accel-Buffering", "no")
	ch := b.subscribe()
	defer b.unsubscribe(ch)
	fmt.Fprint(w, "retry: 3000\n\n")
	flusher.Flush()
	keep := time.NewTicker(25 * time.Second)
	defer keep.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keep.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case ev := <-ch:
			data, _ := json.Marshal(ev.Data)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data)
			flusher.Flush()
		}
	}
}

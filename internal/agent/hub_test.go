package agent

import (
	"context"
	"testing"
	"time"
)

func TestExecRoundTrip(t *testing.T) {
	h := NewHub()
	if _, err := h.Exec(context.Background(), 1, "id", "", time.Second); err != ErrOffline {
		t.Fatal("exec before any poll must report offline")
	}
	// Simulated agent: poll, run, deliver.
	go func() {
		h.Poll(context.Background(), 1, 10*time.Millisecond)
		for i := 0; i < 50; i++ {
			for _, c := range h.Poll(context.Background(), 1, 200*time.Millisecond) {
				h.Deliver(Result{ID: c.ID, Stdout: "ran " + c.Cmd, Code: 0})
				return
			}
		}
	}()
	time.Sleep(50 * time.Millisecond)
	r, err := h.Exec(context.Background(), 1, "uname", "", 5*time.Second)
	if err != nil || r.Stdout != "ran uname" {
		t.Fatalf("%+v %v", r, err)
	}
}

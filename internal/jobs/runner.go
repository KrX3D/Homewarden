// Package jobs runs background work (scans, updates, backups) one job per device at
// a time, with a live log that open browser tabs follow.
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/KrX3D/Homewarden/internal/events"
	"github.com/KrX3D/Homewarden/internal/store"
)

// Handler runs a job. It returns the final status (success/failed/blocked) and a
// result stored with the job.
type Handler func(ctx context.Context, r *Run) (status string, result any, err error)

// Runner executes queued jobs.
type Runner struct {
	st       *store.Store
	events   *events.Broker
	handlers map[string]Handler
	queue    chan int64

	mu      sync.Mutex
	locks   map[int64]*sync.Mutex
	cancels map[int64]context.CancelFunc
}

// New returns a runner; register handlers, then Start it.
func New(st *store.Store, br *events.Broker) *Runner {
	return &Runner{st: st, events: br, handlers: map[string]Handler{}, queue: make(chan int64, 1024),
		locks: map[int64]*sync.Mutex{}, cancels: map[int64]context.CancelFunc{}}
}

// Handle registers the handler for a job type.
func (r *Runner) Handle(typ string, h Handler) { r.handlers[typ] = h }

// Start launches the workers.
func (r *Runner) Start(ctx context.Context, workers int) {
	if err := r.st.FailStaleJobs(ctx); err != nil {
		log.Printf("jobs: %v", err)
	}
	for i := 0; i < workers; i++ {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case id := <-r.queue:
					r.execute(ctx, id)
				}
			}
		}()
	}
}

// Enqueue stores and queues a job.
func (r *Runner) Enqueue(ctx context.Context, j store.Job) (store.Job, error) {
	if _, ok := r.handlers[j.Type]; !ok {
		return j, fmt.Errorf("unknown job type %q", j.Type)
	}
	j, err := r.st.CreateJob(ctx, j)
	if err != nil {
		return j, err
	}
	r.events.Publish("job.status", map[string]any{"id": j.ID, "status": j.Status, "deviceId": j.DeviceID, "type": j.Type})
	r.queue <- j.ID
	return j, nil
}

// Cancel stops a queued or running job.
func (r *Runner) Cancel(id int64) bool {
	r.mu.Lock()
	cancel, ok := r.cancels[id]
	r.mu.Unlock()
	if ok {
		cancel()
	}
	return ok
}

func (r *Runner) deviceLock(id int64) *sync.Mutex {
	r.mu.Lock()
	defer r.mu.Unlock()
	l, ok := r.locks[id]
	if !ok {
		l = &sync.Mutex{}
		r.locks[id] = l
	}
	return l
}

// Run is a job in progress.
type Run struct {
	Job    store.Job
	runner *Runner
	mu     sync.Mutex
}

// Log appends a timestamped line to the job log.
func (run *Run) Log(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	ts := time.Now().Format("15:04:05")
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(msg, "\n"), "\n") {
		b.WriteString(ts + "  " + line + "\n")
	}
	text := b.String()
	run.mu.Lock()
	defer run.mu.Unlock()
	_ = run.runner.st.AppendJobLog(context.Background(), run.Job.ID, text)
	run.runner.events.Publish("job.log", map[string]any{"id": run.Job.ID, "text": text})
}

// Params decodes the job parameters.
func (run *Run) Params(v any) error { return json.Unmarshal(run.Job.Params, v) }

func (r *Runner) execute(parent context.Context, id int64) {
	j, err := r.st.Job(parent, id)
	if err != nil || j.Status != store.JobQueued {
		return
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Hour)
	defer cancel()
	r.mu.Lock()
	r.cancels[id] = cancel
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.cancels, id)
		r.mu.Unlock()
	}()

	// One job per device at a time: an update must not race a scan.
	if j.DeviceID != nil {
		l := r.deviceLock(*j.DeviceID)
		l.Lock()
		defer l.Unlock()
	}
	if ctx.Err() != nil {
		_ = r.st.FinishJob(parent, id, store.JobCancelled, nil)
		r.events.Publish("job.status", map[string]any{"id": id, "status": store.JobCancelled, "deviceId": j.DeviceID, "type": j.Type})
		return
	}
	_ = r.st.StartJob(ctx, id)
	r.events.Publish("job.status", map[string]any{"id": id, "status": store.JobRunning, "deviceId": j.DeviceID, "type": j.Type})
	run := &Run{Job: j, runner: r}
	status, result, err := r.safeRun(ctx, run)
	if err != nil {
		run.Log("Error: %v", err)
		if status == "" || status == store.JobSuccess {
			status = store.JobFailed
		}
	}
	if ctx.Err() == context.Canceled {
		status = store.JobCancelled
		run.Log("Cancelled.")
	}
	if err != nil && status == store.JobFailed {
		switch v := result.(type) {
		case nil:
			result = map[string]any{"error": err.Error()}
		case map[string]any:
			if _, ok := v["error"]; !ok {
				v["error"] = err.Error()
			}
		}
	}
	b, _ := json.Marshal(result)
	_ = r.st.FinishJob(parent, id, status, b)
	r.events.Publish("job.status", map[string]any{"id": id, "status": status, "deviceId": j.DeviceID, "type": j.Type})
}

func (r *Runner) safeRun(ctx context.Context, run *Run) (status string, result any, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("internal error: %v", p)
			status = store.JobFailed
		}
	}()
	return r.handlers[run.Job.Type](ctx, run)
}

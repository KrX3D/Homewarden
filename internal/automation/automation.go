// Package automation runs schedules (updates, backups), remembers actions for devices
// that were offline at the scheduled time, and connects to MQTT for external triggers.
package automation

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/KrX3D/Homewarden/internal/jobs"
	"github.com/KrX3D/Homewarden/internal/store"
)

// Service is the scheduler.
type Service struct {
	St *store.Store
	Sw *jobs.Software
}

// ValidCron reports whether spec is a 5-field cron expression (or a @daily-style
// descriptor).
func ValidCron(spec string) bool {
	_, err := cron.ParseStandard(spec)
	return err == nil
}

// Due reports whether spec fires in the minute starting at t.
func Due(spec string, t time.Time) bool {
	sch, err := cron.ParseStandard(spec)
	if err != nil {
		return false
	}
	m := t.Truncate(time.Minute)
	return sch.Next(m.Add(-time.Second)).Equal(m)
}

// online reports whether actions can run now. Devices without their own check (a
// Docker host reached by API) are always tried.
func online(d store.Device) bool {
	if d.Presence.Reach.Method == "none" && d.ParentID == nil {
		return true
	}
	return d.State == store.StateReady
}

// Trigger runs an action now, or remembers it for when the device is next ready.
func (a *Service) Trigger(ctx context.Context, d store.Device, action, scope, by string) (string, error) {
	if !online(d) {
		b, _ := json.Marshal(map[string]string{"scope": scope, "by": by})
		if err := a.St.AddPending(ctx, store.PendingAction{DeviceID: d.ID, Action: action, Params: string(b)}); err != nil {
			return "", err
		}
		return "pending", nil
	}
	var err error
	switch action {
	case "update":
		_, err = a.Sw.EnqueueScanThen(ctx, d, by, scope)
	case "scan":
		_, err = a.Sw.EnqueueScan(ctx, d, by)
	case "backup":
		_, err = a.Sw.EnqueueBackup(ctx, d, by)
	}
	return "queued", err
}

// OnReady runs actions that waited for the device.
func (a *Service) OnReady(ctx context.Context, d store.Device) {
	pend, err := a.St.TakePending(ctx, d.ID)
	if err != nil {
		return
	}
	for _, p := range pend {
		var params struct{ Scope, By string }
		_ = json.Unmarshal([]byte(p.Params), &params)
		if params.By == "" {
			params.By = "schedule"
		}
		d.State = store.StateReady
		if _, err := a.Trigger(ctx, d, p.Action, params.Scope, params.By+" (was offline)"); err != nil {
			log.Printf("pending %s for %s: %v", p.Action, d.Name, err)
		}
	}
}

// Run checks schedules once a minute.
func (a *Service) Run(ctx context.Context) {
	for {
		now := time.Now()
		next := now.Truncate(time.Minute).Add(time.Minute)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
		}
		a.tick(ctx, next)
	}
}

func (a *Service) tick(ctx context.Context, t time.Time) {
	devs, err := a.St.Devices(ctx, store.StatusActive)
	if err != nil {
		return
	}
	for _, d := range devs {
		if p := jobs.DevicePolicy(d); p.Mode == "scheduled" && p.Schedule != "" && Due(p.Schedule, t) {
			if r, err := a.Trigger(ctx, d, "update", p.Scope, "schedule"); err != nil {
				log.Printf("scheduled update %s: %v", d.Name, err)
			} else {
				log.Printf("scheduled update %s: %s", d.Name, r)
			}
		}
		if b := jobs.DeviceBackup(d); b.Schedule != "" && b.TargetID != 0 && Due(b.Schedule, t) {
			if _, err := a.Trigger(ctx, d, "backup", "", "schedule"); err != nil {
				log.Printf("scheduled backup %s: %v", d.Name, err)
			}
		}
	}
}

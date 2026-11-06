package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/KrX3D/Homewarden/internal/events"
	"github.com/KrX3D/Homewarden/internal/integrations"
	"github.com/KrX3D/Homewarden/internal/store"
)

// Software wires integrations into jobs: scans, updates and discovery of child
// devices (e.g. containers on a Docker host).
type Software struct {
	St     *store.Store
	Reg    *integrations.Registry
	Host   *integrations.Host
	Runner *Runner
	Events *events.Broker
}

// Register adds the scan and update job types.
func (s *Software) Register() {
	s.Runner.Handle("scan", s.scan)
	s.Runner.Handle("update", s.update)
}

// detectResult is what an integration's detect() returns.
type detectResult struct {
	Facts      json.RawMessage `json:"facts"`
	Components []struct {
		ID              string          `json:"id"`
		Name            string          `json:"name"`
		Group           string          `json:"group"`
		Installed       string          `json:"installed"`
		Available       string          `json:"available"`
		UpdateAvailable *bool           `json:"updateAvailable"`
		Security        bool            `json:"security"`
		Updatable       *bool           `json:"updatable"`
		Meta            json.RawMessage `json:"meta"`
	} `json:"components"`
}

// discovered is one child device returned by discover().
type discovered struct {
	ExternalID string                `json:"externalId"`
	Name       string                `json:"name"`
	Host       string                `json:"host"`
	Kind       string                `json:"kind"`
	State      string                `json:"state"` // running | stopped
	Group      string                `json:"group"`
	Presence   *store.PresenceConfig `json:"presence"`
}

func (s *Software) integration(d store.Device) (*integrations.Integration, error) {
	in, ok := s.Reg.Get(d.Kind)
	if !ok {
		return nil, fmt.Errorf("no installed integration %q for this device", d.Kind)
	}
	return in, nil
}

// EnqueueScan queues a scan unless one is already queued or running for the device.
func (s *Software) EnqueueScan(ctx context.Context, d store.Device, by string) (store.Job, error) {
	if last, err := s.St.LastJob(ctx, d.ID, "scan"); err == nil && (last.Status == store.JobQueued || last.Status == store.JobRunning) {
		return last, nil
	}
	id := d.ID
	return s.Runner.Enqueue(ctx, store.Job{Type: "scan", DeviceID: &id, DeviceName: d.Name, Title: "Scan " + d.Name, CreatedBy: by})
}

func (s *Software) scan(ctx context.Context, run *Run) (string, any, error) {
	d, err := s.St.Device(ctx, *run.Job.DeviceID)
	if err != nil {
		return store.JobFailed, nil, err
	}
	in, err := s.integration(d)
	if err != nil {
		return store.JobFailed, nil, err
	}
	run.Log("Scanning %s with %s %s", d.Name, in.ID, in.Version)
	res, err := s.detect(ctx, run, in, d)
	if err != nil {
		_ = s.St.SetFacts(ctx, d.ID, nil, err.Error())
		s.Events.Publish("device.software", map[string]int64{"id": d.ID})
		return store.JobFailed, nil, err
	}
	if in.Has("discover") {
		if n, err := s.discover(ctx, run, in, d); err != nil {
			run.Log("Discovery failed: %v", err)
		} else if n > 0 {
			run.Log("%d new devices are waiting in the inbox.", n)
		}
	}
	return store.JobSuccess, res, nil
}

// detect runs detect() and stores components and facts. Returns a summary.
func (s *Software) detect(ctx context.Context, run *Run, in *integrations.Integration, d store.Device) (map[string]int, error) {
	raw, err := s.Host.Call(ctx, in, d, "detect", nil, func(l string) { run.Log("%s", l) })
	if err != nil {
		return nil, err
	}
	var res detectResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("detect() returned an unexpected shape: %w", err)
	}
	comps := make([]store.Component, 0, len(res.Components))
	seen := map[string]bool{}
	updates, security := 0, 0
	for _, c := range res.Components {
		if c.ID == "" || seen[c.ID] {
			continue
		}
		seen[c.ID] = true
		upd := c.Available != "" && c.Available != c.Installed
		if c.UpdateAvailable != nil {
			upd = *c.UpdateAvailable
		}
		updatable := true
		if c.Updatable != nil {
			updatable = *c.Updatable
		}
		if upd {
			updates++
			if c.Security {
				security++
			}
		}
		name := c.Name
		if name == "" {
			name = c.ID
		}
		comps = append(comps, store.Component{ID: c.ID, Name: name, Group: c.Group, Installed: c.Installed, Available: c.Available,
			UpdateAvailable: upd, Security: c.Security, Updatable: updatable, Meta: c.Meta})
	}
	changed, err := s.St.ReplaceComponents(ctx, d.ID, comps)
	if err != nil {
		return nil, err
	}
	if err := s.St.SetFacts(ctx, d.ID, res.Facts, ""); err != nil {
		return nil, err
	}
	run.Log("Found %d components, %d with updates (%d security).", len(comps), updates, security)
	if changed > 0 {
		run.Log("%d components changed version since the last scan.", changed)
	}
	s.Events.Publish("device.software", map[string]int64{"id": d.ID})
	return map[string]int{"components": len(comps), "updates": updates, "security": security, "changed": changed}, nil
}

func (s *Software) discover(ctx context.Context, run *Run, in *integrations.Integration, parent store.Device) (int, error) {
	raw, err := s.Host.Call(ctx, in, parent, "discover", nil, func(l string) { run.Log("%s", l) })
	if err != nil {
		return 0, err
	}
	var found []discovered
	if err := json.Unmarshal(raw, &found); err != nil {
		return 0, err
	}
	source := fmt.Sprintf("%s:%d", in.ID, parent.ID)
	created := 0
	for _, f := range found {
		if f.ExternalID == "" {
			continue
		}
		state := store.StateOffline
		if f.State == "running" {
			state = store.StateReady
		}
		existing, err := s.St.DeviceByExternal(ctx, source, f.ExternalID)
		if errors.Is(err, store.ErrNotFound) {
			pid := parent.ID
			pres := store.PresenceConfig{Reach: store.ProbeConfig{Method: "none"}, Ready: store.ProbeConfig{Method: "none"}, IntervalSeconds: 60}
			if f.Presence != nil {
				pres = *f.Presence
			}
			kind := f.Kind
			if kind == "" {
				kind = "generic"
			}
			nd, err := s.St.CreateDevice(ctx, store.Device{Name: f.Name, Kind: kind, Host: f.Host, ParentID: &pid, GroupName: firstNonEmpty(f.Group, parent.GroupName),
				Status: store.StatusDiscovered, Source: source, ExternalID: f.ExternalID, Presence: pres})
			if err != nil {
				return created, err
			}
			_ = s.St.SetDeviceState(ctx, nd.ID, state, time.Now().Unix())
			created++
			continue
		}
		if err != nil {
			return created, err
		}
		// Keep name and state of known children current; their presence comes from here.
		if existing.Presence.Reach.Method == "none" && existing.State != state {
			_ = s.St.SetDeviceState(ctx, existing.ID, state, time.Now().Unix())
			s.Events.Publish("device.state", map[string]any{"id": existing.ID, "state": state, "since": time.Now().Unix(), "previous": existing.State})
		}
		if state == store.StateReady {
			_ = s.St.RecordSeen(ctx, existing.ID, time.Now().Unix(), nil)
		}
	}
	if created > 0 {
		s.Events.Publish("device.changed", map[string]int64{"id": parent.ID})
	}
	return created, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// UpdateParams are the parameters of an update job.
type UpdateParams struct {
	Components []string `json:"components"` // ids, or ["*"] for everything
	Force      bool     `json:"force"`      // run even when a busy guard says no
}

func (s *Software) update(ctx context.Context, run *Run) (string, any, error) {
	d, err := s.St.Device(ctx, *run.Job.DeviceID)
	if err != nil {
		return store.JobFailed, nil, err
	}
	in, err := s.integration(d)
	if err != nil {
		return store.JobFailed, nil, err
	}
	var p UpdateParams
	if err := run.Params(&p); err != nil || len(p.Components) == 0 {
		return store.JobFailed, nil, errors.New("no components selected")
	}
	if !in.Has("update") {
		return store.JobFailed, nil, errors.New("this integration cannot run updates")
	}
	if in.Has("busy") {
		raw, err := s.Host.Call(ctx, in, d, "busy", nil, func(l string) { run.Log("%s", l) })
		if err != nil {
			run.Log("Could not check whether the device is busy: %v", err)
			if !p.Force {
				return store.JobBlocked, map[string]string{"reason": "busy check failed: " + err.Error()}, nil
			}
		} else {
			var b struct {
				Busy   bool   `json:"busy"`
				Reason string `json:"reason"`
			}
			_ = json.Unmarshal(raw, &b)
			if b.Busy && !p.Force {
				run.Log("Not updating: %s", b.Reason)
				return store.JobBlocked, map[string]string{"reason": b.Reason}, nil
			}
			if b.Busy {
				run.Log("Device is busy (%s), updating anyway because it was forced.", b.Reason)
			}
		}
	}
	before, _ := s.St.Components(ctx, d.ID)
	run.Log("Updating %s on %s", strings.Join(p.Components, ", "), d.Name)
	raw, err := s.Host.Call(ctx, in, d, "update", p.Components, func(l string) { run.Log("%s", l) })
	if err != nil {
		run.Log("Update failed. Scanning again to show the current state.")
		_, _ = s.detect(ctx, run, in, d)
		return store.JobFailed, nil, err
	}
	run.Log("Update finished. Scanning again.")
	summary, err := s.detect(ctx, run, in, d)
	if err != nil {
		run.Log("Rescan failed: %v", err)
	} else {
		s.recordUpdated(ctx, run, d.ID, before, p.Components)
	}
	var result any
	_ = json.Unmarshal(raw, &result)
	return store.JobSuccess, map[string]any{"update": result, "scan": summary}, nil
}

// recordUpdated writes version history for components that had an update before and
// no longer do. Scans often stop listing a component once it is current (apt only
// lists upgradable packages), so the scan itself cannot see these changes.
func (s *Software) recordUpdated(ctx context.Context, run *Run, deviceID int64, before []store.Component, selected []string) {
	all := len(selected) == 1 && selected[0] == "*"
	sel := map[string]bool{}
	for _, id := range selected {
		sel[id] = true
	}
	after, err := s.St.Components(ctx, deviceID)
	if err != nil {
		return
	}
	now := map[string]store.Component{}
	for _, c := range after {
		now[c.ID] = c
	}
	n := 0
	for _, b := range before {
		if !b.UpdateAvailable || b.Available == "" || (!all && !sel[b.ID]) || b.Group == "os" || b.Group == "action" {
			continue
		}
		a, still := now[b.ID]
		if still && a.UpdateAvailable && a.Installed == b.Installed {
			continue // not updated
		}
		newV := b.Available
		if still && a.Installed != "" {
			newV = a.Installed
		}
		if still && a.Installed != b.Installed {
			continue // the scan already recorded this change
		}
		if s.St.AddVersionChange(ctx, deviceID, store.VersionChange{ComponentID: b.ID, Name: b.Name, OldVersion: b.Installed, NewVersion: newV}) == nil {
			n++
		}
	}
	if n > 0 {
		run.Log("%d components updated.", n)
	}
}

// AutoScan queues scans for devices that became ready, and rescans ready devices
// every few hours.
func (s *Software) AutoScan(ctx context.Context) {
	t := time.NewTicker(15 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = s.St.PruneHistory(ctx, time.Now().AddDate(-1, 0, 0).Unix())
			devs, err := s.St.Devices(ctx, store.StatusActive)
			if err != nil {
				continue
			}
			for _, d := range devs {
				if d.State != store.StateReady {
					continue
				}
				if _, ok := s.Reg.Get(d.Kind); !ok {
					continue
				}
				if last, err := s.St.LastJob(ctx, d.ID, "scan"); err == nil && time.Since(time.Unix(last.CreatedAt, 0)) < 6*time.Hour {
					continue
				}
				if _, err := s.EnqueueScan(ctx, d, "schedule"); err != nil {
					log.Printf("autoscan %s: %v", d.Name, err)
				}
			}
		}
	}
}

// OnReady is hooked to presence: a device that just finished booting gets scanned,
// unless it was scanned in the last 10 minutes.
func (s *Software) OnReady(ctx context.Context, d store.Device) {
	if d.Status != store.StatusActive {
		return
	}
	if _, ok := s.Reg.Get(d.Kind); !ok {
		return
	}
	if last, err := s.St.LastJob(ctx, d.ID, "scan"); err == nil && time.Since(time.Unix(last.CreatedAt, 0)) < 10*time.Minute {
		return
	}
	if _, err := s.EnqueueScan(ctx, d, "presence"); err != nil {
		log.Printf("scan on ready %s: %v", d.Name, err)
	}
}

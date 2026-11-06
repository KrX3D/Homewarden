package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/KrX3D/Homewarden/internal/store"
)

// MetricsInterval is how often ready devices are sampled.
const MetricsInterval = 5 * time.Minute

// smartEvery: SMART data is read on every n-th sample (once an hour), since reading it
// wakes sleeping disks less often and takes longer.
const smartEvery = 12

// Thresholds that turn a sample into a warning.
const (
	diskFullPct = 90.0
	hotCelsius  = 80.0
)

// metricsSample is the part of a metrics() result the core evaluates.
type metricsSample struct {
	Disks []struct {
		Mount   string  `json:"mount"`
		UsedPct float64 `json:"usedPct"`
	} `json:"disks"`
	Temps []struct {
		Name string  `json:"name"`
		C    float64 `json:"c"`
	} `json:"temps"`
	Smart []struct {
		Dev     string `json:"dev"`
		Healthy *bool  `json:"healthy"`
	} `json:"smart"`
}

// Warnings derives warning codes from a sample.
func (m metricsSample) Warnings() []string {
	out := []string{}
	for _, d := range m.Disks {
		if d.UsedPct >= diskFullPct {
			out = append(out, "disk_full")
			break
		}
	}
	for _, t := range m.Temps {
		if t.C >= hotCelsius {
			out = append(out, "hot")
			break
		}
	}
	for _, s := range m.Smart {
		if s.Healthy != nil && !*s.Healthy {
			out = append(out, "smart_failing")
			break
		}
	}
	return out
}

// SampleDevice takes one sample now and stores it.
func (s *Software) SampleDevice(ctx context.Context, d store.Device, smart bool) error {
	in, ok := s.Reg.Get(d.Kind)
	if !ok || !in.Has("metrics") {
		return errors.New("this integration has no resource metrics")
	}
	cctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	raw, err := s.Host.Call(cctx, in, d, "metrics", map[string]bool{"smart": smart}, nil)
	if err != nil {
		return err
	}
	if err := s.St.AddMetrics(ctx, d.ID, raw); err != nil {
		return err
	}
	var m metricsSample
	_ = json.Unmarshal(raw, &m)
	s.Events.Publish("device.metrics", map[string]any{"id": d.ID, "warnings": m.Warnings()})
	return nil
}

// MetricsLoop samples every ready device whose integration supports metrics.
func (s *Software) MetricsLoop(ctx context.Context) {
	var mu sync.Mutex
	runs := map[int64]int{}
	t := time.NewTicker(MetricsInterval)
	defer t.Stop()
	prune := time.NewTicker(12 * time.Hour)
	defer prune.Stop()
	sample := func() {
		devs, err := s.St.Devices(ctx, store.StatusActive)
		if err != nil {
			return
		}
		sem := make(chan struct{}, 4)
		var wg sync.WaitGroup
		for _, d := range devs {
			in, ok := s.Reg.Get(d.Kind)
			if !ok || !in.Has("metrics") {
				continue
			}
			if d.State != store.StateReady && !(d.Presence.Reach.Method == "none" && d.ParentID == nil) {
				continue
			}
			var cfg struct {
				Metrics *bool `json:"metrics"`
			}
			_ = json.Unmarshal(d.Config, &cfg)
			if cfg.Metrics != nil && !*cfg.Metrics {
				continue
			}
			mu.Lock()
			n := runs[d.ID]
			runs[d.ID] = n + 1
			mu.Unlock()
			wg.Add(1)
			sem <- struct{}{}
			go func(d store.Device, smart bool) {
				defer wg.Done()
				defer func() { <-sem }()
				if err := s.SampleDevice(ctx, d, smart); err != nil {
					log.Printf("metrics %s: %v", d.Name, err)
				}
			}(d, n%smartEvery == 0)
		}
		wg.Wait()
	}
	sample()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sample()
		case <-prune.C:
			_ = s.St.PruneMetrics(ctx, time.Now().AddDate(0, 0, -30).Unix())
		}
	}
}

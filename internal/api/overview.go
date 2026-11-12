package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/KrX3D/Homewarden/internal/auth"
	"github.com/KrX3D/Homewarden/internal/monitor"
	"github.com/KrX3D/Homewarden/internal/store"
)

// The overview page: what is wrong, what waits for the user, today's figures, each
// area's checks and the last seven days, from data Homewarden already has.

// Areas group devices and monitors like the overview shows them.
const (
	AreaServers = "servers"
	AreaNetwork = "network"
	AreaStorage = "storage"
	AreaApps    = "apps"
)

var areaOrder = []string{AreaServers, AreaNetwork, AreaStorage, AreaApps}

// DeviceArea derives a device's area from its integration.
func DeviceArea(kind string) string {
	switch kind {
	case "proxmox", "unraid", "linux-apt", "windows", "deploydeck", "docker":
		return AreaServers
	case "unifi":
		return AreaNetwork
	case "proxmox-backup":
		return AreaStorage
	}
	return AreaApps
}

// monitorArea: the monitor's own choice, else its device's area, else by type.
func monitorArea(m store.Monitor, cfg monitor.Config, devices map[int64]store.Device) string {
	switch cfg.Area {
	case AreaServers, AreaNetwork, AreaStorage, AreaApps:
		return cfg.Area
	}
	if m.DeviceID != nil {
		if d, ok := devices[*m.DeviceID]; ok {
			return DeviceArea(d.Kind)
		}
	}
	switch m.Type {
	case "dns":
		return AreaNetwork
	case "ping", "tcp":
		return AreaServers
	}
	return AreaApps
}

type ovProblem struct {
	Kind     string `json:"kind"`     // monitor | offline | warning | metric | scan | job
	Severity string `json:"severity"` // crit | warn
	Name     string `json:"name"`
	Area     string `json:"area"`
	Code     string `json:"code,omitempty"` // warning code, the UI explains it
	Why      string `json:"why,omitempty"`  // raw message
	Since    *int64 `json:"since,omitempty"`
	DeviceID *int64 `json:"deviceId,omitempty"`
	JobID    *int64 `json:"jobId,omitempty"`
}

type ovCert struct {
	MonitorID int64  `json:"monitorId"`
	Name      string `json:"name"`
	Expires   int64  `json:"expires"`
}

type ovChores struct {
	Updates            int      `json:"updates"`
	Security           int      `json:"security"`
	UpdateDevices      int      `json:"updateDevices"`
	Inbox              int      `json:"inbox"`
	IntegrationUpdates int      `json:"integrationUpdates"`
	Reboot             []string `json:"reboot"`
	Certs              []ovCert `json:"certs"`
}

type ovCount struct {
	Up    int `json:"up"`
	Total int `json:"total"`
}

type ovDisk struct {
	Device  string  `json:"device"`
	Mount   string  `json:"mount"`
	UsedPct float64 `json:"usedPct"`
	SizeGB  float64 `json:"sizeGB"`
}

type ovToday struct {
	Devices        ovCount   `json:"devices"`
	Sometimes      int       `json:"sometimes"` // offline, but nobody expects them on (no offline alert)
	Monitors       ovCount   `json:"monitors"`
	AvgMs          *float64  `json:"avgMs"`
	Uptime24h      float64   `json:"uptime24h"`
	MsSeries       []float64 `json:"msSeries"` // average response per hour, last 24 h
	Guests         ovCount   `json:"guests"`
	Containers     ovCount   `json:"containers"`
	Updates        int       `json:"updates"`
	Security       int       `json:"security"`
	InstalledToday int       `json:"installedToday"`
	Backups        ovCount   `json:"backups"` // devices with a backup set up / with one in the last 8 days
	LastBackup     *int64    `json:"lastBackup"`
	Disks          []ovDisk  `json:"disks"` // fullest file systems
}

type ovItem struct {
	ID     int64    `json:"id"`
	Name   string   `json:"name"`
	Type   string   `json:"type"`
	Status string   `json:"status"`
	Ms     *float64 `json:"ms"`
	Ticks  []int    `json:"ticks"` // last 30 results, oldest first: 1 ok, 0 failed
}

type ovArea struct {
	ID     string   `json:"id"`
	Health string   `json:"health"` // up | degraded | down | unknown
	Up     int      `json:"up"`
	Total  int      `json:"total"`
	Ms     *float64 `json:"ms"`
	Ticks  []int    `json:"ticks"` // -1 no data, 1 all passed, 0 all failed, 2 some failed
	Items  []ovItem `json:"items"`
}

type ovEvent struct {
	Kind     string `json:"kind"` // outage | update | backup | failed
	Area     string `json:"area"`
	Title    string `json:"title"`
	At       int64  `json:"at"`
	Until    *int64 `json:"until,omitempty"` // outages; nil = still down
	DeviceID *int64 `json:"deviceId,omitempty"`
	JobID    *int64 `json:"jobId,omitempty"`
}

// AttachOverview registers GET /api/overview. Call after software and monitors.
func (s *Server) AttachOverview() {
	s.mux.Handle("GET /api/overview", s.authed(auth.PermDevicesView, s.overview))
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	out, err := s.buildOverview(r)
	if err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// buildOverview collects the overview for the request's user (also used by MCP).
func (s *Server) buildOverview(r *http.Request) (map[string]any, error) {
	ctx := r.Context()
	p := PrincipalFrom(r)
	now := time.Now()
	all, err := s.Store.Devices(ctx, "")
	if err != nil {
		return nil, err
	}
	devices := map[int64]store.Device{}
	active := []store.Device{}
	inbox := 0
	for _, d := range all {
		if !p.SeesDevice(d) {
			continue
		}
		devices[d.ID] = d
		switch d.Status {
		case store.StatusActive:
			active = append(active, d)
		case store.StatusDiscovered:
			inbox++
		}
	}
	problems := []ovProblem{}
	chores := ovChores{Inbox: inbox, Reboot: []string{}, Certs: []ovCert{}}
	today := ovToday{Disks: []ovDisk{}, MsSeries: []float64{}}

	// Devices: presence, scan warnings, resource warnings.
	for _, d := range active {
		id := d.ID
		area := DeviceArea(d.Kind)
		parentKind := ""
		if d.ParentID != nil {
			parentKind = devices[*d.ParentID].Kind
		}
		on := d.State == store.StateReady || d.State == store.StateReachable
		switch parentKind {
		case "docker", "unraid":
			today.Containers.Total++
			if on {
				today.Containers.Up++
			}
		case "proxmox":
			today.Guests.Total++
			if on {
				today.Guests.Up++
			}
		}
		if d.State != store.StatePaused {
			today.Devices.Total++
			if on {
				today.Devices.Up++
			}
		}
		var cfg struct {
			Notify struct {
				OfflineMinutes int `json:"offlineMinutes"`
			} `json:"notify"`
			Backup struct {
				TargetID *int64 `json:"targetId"`
			} `json:"backup"`
		}
		_ = json.Unmarshal(d.Config, &cfg)
		if d.State == store.StateOffline {
			if cfg.Notify.OfflineMinutes > 0 && d.StateSince != nil && now.Unix()-*d.StateSince >= int64(cfg.Notify.OfflineMinutes)*60 {
				problems = append(problems, ovProblem{Kind: "offline", Severity: "crit", Name: d.Name, Area: area, Since: d.StateSince, DeviceID: &id})
			} else if parentKind == "" {
				today.Sometimes++
			}
		}
		if cfg.Backup.TargetID != nil {
			today.Backups.Total++
			if bs, err := s.Store.Backups(ctx, d.ID); err == nil && len(bs) > 0 {
				newest := bs[0].At
				for _, b := range bs {
					newest = max(newest, b.At)
				}
				if now.Unix()-newest < 8*86400 {
					today.Backups.Up++
				}
				if today.LastBackup == nil || newest > *today.LastBackup {
					n := newest
					today.LastBackup = &n
				}
			}
		}
		if f, err := s.Store.DeviceFacts(ctx, d.ID); err == nil {
			var facts struct {
				Warnings       []string `json:"warnings"`
				EOLPassed      bool     `json:"eolPassed"`
				RebootRequired bool     `json:"rebootRequired"`
			}
			_ = json.Unmarshal(f.Facts, &facts)
			if facts.EOLPassed {
				facts.Warnings = append(facts.Warnings, "eol")
			}
			for _, code := range facts.Warnings {
				problems = append(problems, ovProblem{Kind: "warning", Severity: "warn", Name: d.Name, Area: area, Code: code, DeviceID: &id})
			}
			if f.Error != "" && on {
				at := f.ScannedAt
				problems = append(problems, ovProblem{Kind: "scan", Severity: "warn", Name: d.Name, Area: area, Why: f.Error, Since: &at, DeviceID: &id})
			}
			if facts.RebootRequired {
				chores.Reboot = append(chores.Reboot, d.Name)
			}
		}
		if mp, err := s.Store.LatestMetrics(ctx, d.ID); err == nil && now.Unix()-mp.At < 3600 {
			var m struct {
				Disks []struct {
					Mount   string  `json:"mount"`
					UsedPct float64 `json:"usedPct"`
					SizeGB  float64 `json:"sizeGB"`
				} `json:"disks"`
				Temps []struct {
					C float64 `json:"c"`
				} `json:"temps"`
				Smart []struct {
					Dev     string `json:"dev"`
					Healthy *bool  `json:"healthy"`
				} `json:"smart"`
			}
			_ = json.Unmarshal(mp.Data, &m)
			full, hot := false, false
			for _, dk := range m.Disks {
				today.Disks = append(today.Disks, ovDisk{d.Name, dk.Mount, dk.UsedPct, dk.SizeGB})
				full = full || dk.UsedPct >= 90
			}
			for _, t := range m.Temps {
				hot = hot || t.C >= 80
			}
			if full {
				problems = append(problems, ovProblem{Kind: "metric", Severity: "warn", Name: d.Name, Area: AreaStorage, Code: "disk_full", DeviceID: &id})
			}
			if hot {
				problems = append(problems, ovProblem{Kind: "metric", Severity: "warn", Name: d.Name, Area: area, Code: "hot", DeviceID: &id})
			}
			for _, sm := range m.Smart {
				if sm.Healthy != nil && !*sm.Healthy {
					problems = append(problems, ovProblem{Kind: "metric", Severity: "crit", Name: d.Name, Area: AreaStorage, Code: "smart_failing", Why: sm.Dev, DeviceID: &id})
				}
			}
		}
	}
	sort.Slice(today.Disks, func(i, j int) bool { return today.Disks[i].UsedPct > today.Disks[j].UsedPct })
	if len(today.Disks) > 3 {
		today.Disks = today.Disks[:3]
	}

	// Updates.
	if counts, err := s.Store.UpdateCounts(ctx); err == nil {
		for id, c := range counts {
			if _, ok := devices[id]; !ok || devices[id].Status != store.StatusActive || c[0] == 0 {
				continue
			}
			chores.Updates += c[0]
			chores.Security += c[1]
			chores.UpdateDevices++
		}
	}
	today.Updates, today.Security = chores.Updates, chores.Security
	if p.Can(auth.PermIntegrationsManage) && s.sw.Registry != nil {
		entries, _, _ := s.cachedCatalog(ctx, false)
		chores.IntegrationUpdates = len(s.integrationUpdatesFrom(entries))
	}

	// Monitors: problems, areas, outages.
	mons, _ := s.Store.Monitors(ctx)
	areas := map[string]*ovArea{}
	events := []ovEvent{}
	weekAgo := now.AddDate(0, 0, -7).Unix()
	var msSum float64
	var msN int
	hourSum := make([]float64, 24)
	hourN := make([]int, 24)
	var upSum float64
	var upN int
	for _, m := range mons {
		if !m.Enabled {
			continue
		}
		if m.DeviceID != nil {
			if _, ok := devices[*m.DeviceID]; !ok {
				if d, err := s.Store.Device(ctx, *m.DeviceID); err == nil && !p.SeesDevice(d) {
					continue
				}
			}
		}
		var cfg monitor.Config
		_ = json.Unmarshal(m.Config, &cfg)
		area := monitorArea(m, cfg, devices)
		today.Monitors.Total++
		if m.Status == "up" {
			today.Monitors.Up++
			if m.LastMs != nil {
				msSum += *m.LastMs
				msN++
			}
		}
		if m.Status == "down" {
			problems = append(problems, ovProblem{Kind: "monitor", Severity: "crit", Name: m.Name, Area: area, Why: m.LastMsg, Since: m.StatusSince, DeviceID: m.DeviceID})
		}
		if m.CertExpires != nil && *m.CertExpires-now.Unix() < 30*86400 {
			chores.Certs = append(chores.Certs, ovCert{m.ID, m.Name, *m.CertExpires})
		}
		res, _ := s.Store.MonitorResults(ctx, m.ID, weekAgo)
		if u, err := s.Store.MonitorUptime(ctx, m.ID, now.Add(-24*time.Hour).Unix()); err == nil && u >= 0 {
			upSum += u
			upN++
		}
		// Outages: runs of failed results long enough to mark the monitor down.
		need := cfg.FailuresForDown()
		run, start := 0, int64(0)
		for _, pt := range res {
			if !pt.OK {
				if run == 0 {
					start = pt.At
				}
				run++
				continue
			}
			if run >= need {
				end := pt.At
				events = append(events, ovEvent{Kind: "outage", Area: area, Title: m.Name, At: start, Until: &end, DeviceID: m.DeviceID})
			}
			run = 0
		}
		if run >= need {
			events = append(events, ovEvent{Kind: "outage", Area: area, Title: m.Name, At: start, DeviceID: m.DeviceID})
		}
		dayAgo := now.Add(-24 * time.Hour).Unix()
		for _, pt := range res {
			if pt.At >= dayAgo && pt.OK && pt.Ms != nil {
				h := min(23, int((pt.At-dayAgo)/3600))
				hourSum[h] += *pt.Ms
				hourN[h]++
			}
		}
		ticks := []int{}
		from := max(0, len(res)-30)
		for _, pt := range res[from:] {
			if pt.OK {
				ticks = append(ticks, 1)
			} else {
				ticks = append(ticks, 0)
			}
		}
		a := areas[area]
		if a == nil {
			a = &ovArea{ID: area, Items: []ovItem{}}
			areas[area] = a
		}
		a.Items = append(a.Items, ovItem{m.ID, m.Name, m.Type, m.Status, m.LastMs, ticks})
	}
	if msN > 0 {
		v := msSum / float64(msN)
		today.AvgMs = &v
	}
	today.Uptime24h = -1
	if upN > 0 {
		today.Uptime24h = upSum / float64(upN)
	}
	for h := range hourSum {
		if hourN[h] > 0 {
			today.MsSeries = append(today.MsSeries, hourSum[h]/float64(hourN[h]))
		}
	}
	outAreas := []ovArea{}
	for _, id := range areaOrder {
		a := areas[id]
		if a == nil {
			continue
		}
		var ms float64
		var msC int
		down := 0
		okN, failN := make([]int, 30), make([]int, 30)
		for _, it := range a.Items {
			a.Total++
			if it.Status == "up" {
				a.Up++
				if it.Ms != nil {
					ms += *it.Ms
					msC++
				}
			} else if it.Status == "down" || it.Status == "dependency" {
				down++
			}
			// Align each item's ticks to the right (newest last).
			off := 30 - len(it.Ticks)
			for i, v := range it.Ticks {
				if v == 1 {
					okN[off+i]++
				} else {
					failN[off+i]++
				}
			}
		}
		// A slot: -1 no data, 1 all passed, 0 all failed, 2 some failed.
		a.Ticks = make([]int, 30)
		for i := range a.Ticks {
			switch {
			case okN[i] == 0 && failN[i] == 0:
				a.Ticks[i] = -1
			case failN[i] == 0:
				a.Ticks[i] = 1
			case okN[i] == 0:
				a.Ticks[i] = 0
			default:
				a.Ticks[i] = 2
			}
		}
		switch {
		case a.Total > 0 && down == a.Total:
			a.Health = "down"
		case down > 0:
			a.Health = "degraded"
		case a.Up == a.Total:
			a.Health = "up"
		default:
			a.Health = "unknown"
		}
		if msC > 0 {
			v := ms / float64(msC)
			a.Ms = &v
		}
		outAreas = append(outAreas, *a)
	}

	// Jobs of the last seven days: updates, backups, failures.
	if jobs, err := s.Store.JobsSince(ctx, weekAgo); err == nil {
		midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()
		latestFailed := map[string]bool{}
		for _, j := range jobs { // newest first
			if j.DeviceID != nil {
				if d, ok := devices[*j.DeviceID]; !ok || d.Status != store.StatusActive {
					continue
				}
			}
			area := AreaApps
			if j.DeviceID != nil {
				area = DeviceArea(devices[*j.DeviceID].Kind)
			}
			at := j.CreatedAt
			if j.FinishedAt != nil {
				at = *j.FinishedAt
			}
			id := j.ID
			key := j.Type
			if j.DeviceID != nil {
				key += ":" + devices[*j.DeviceID].Name
			}
			switch {
			case (j.Type == "update" || j.Type == "backup") && j.Status == store.JobSuccess:
				events = append(events, ovEvent{Kind: j.Type, Area: area, Title: j.Title, At: at, DeviceID: j.DeviceID, JobID: &id})
				if j.Type == "update" && at >= midnight {
					today.InstalledToday++
				}
				latestFailed[key] = false
			case (j.Type == "update" || j.Type == "backup" || j.Type == "restore") && (j.Status == store.JobFailed || j.Status == store.JobBlocked):
				events = append(events, ovEvent{Kind: "failed", Area: area, Title: j.Title, At: at, DeviceID: j.DeviceID, JobID: &id})
				// Only the newest job of a kind per device counts as an open problem.
				if _, seen := latestFailed[key]; !seen && now.Unix()-at < 86400 {
					var res struct {
						Error  string `json:"error"`
						Reason string `json:"reason"`
					}
					_ = json.Unmarshal(j.Result, &res)
					why := res.Error
					if why == "" {
						why = res.Reason
					}
					problems = append(problems, ovProblem{Kind: "job", Severity: "warn", Name: j.Title, Area: area, Why: why, Since: &at, DeviceID: j.DeviceID, JobID: &id})
				}
				latestFailed[key] = true
			}
		}
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].At > events[j].At })
	sort.SliceStable(problems, func(i, j int) bool {
		return problems[i].Severity == "crit" && problems[j].Severity != "crit"
	})
	return map[string]any{
		"problems": problems, "chores": chores, "today": today, "areas": outAreas, "events": events, "at": now.Unix(),
	}, nil
}

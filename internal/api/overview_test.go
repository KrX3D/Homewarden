package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/KrX3D/Homewarden/internal/store"
)

func TestOverview(t *testing.T) {
	h := newHarness(t)
	h.api.AttachOverview()
	c := h.client()
	if code, _ := h.do(c, "POST", "/api/setup", map[string]string{"token": h.token, "username": "krx", "password": "a-long-password"}); code != 200 {
		t.Fatal("setup")
	}
	ctx := context.Background()
	st := h.api.Store
	ago := time.Now().Add(-time.Hour).Unix()

	host, _ := st.CreateDevice(ctx, store.Device{Name: "Unraid", Kind: "docker", Status: store.StatusActive})
	st.SetDeviceState(ctx, host.ID, store.StateReady, ago)
	app, _ := st.CreateDevice(ctx, store.Device{Name: "gitea", ParentID: &host.ID, Status: store.StatusActive})
	st.SetDeviceState(ctx, app.ID, store.StateOffline, ago)
	pve, _ := st.CreateDevice(ctx, store.Device{Name: "Proxmox", Kind: "proxmox", Status: store.StatusActive,
		Config: json.RawMessage(`{"notify":{"offlineMinutes":5}}`)})
	st.SetDeviceState(ctx, pve.ID, store.StateOffline, ago)
	st.SetFacts(ctx, host.ID, json.RawMessage(`{"warnings":["disk_problems"],"rebootRequired":true}`), "")
	st.CreateDevice(ctx, store.Device{Name: "new", Status: store.StatusDiscovered})

	m, _ := st.SaveMonitor(ctx, store.Monitor{Name: "Home Assistant", Type: "http", Enabled: true, Interval: 60, DeviceID: &app.ID, Config: json.RawMessage(`{}`)})
	for _, ok := range []bool{true, false, false, false} {
		st.RecordMonitor(ctx, m.ID, ok, nil, "certificate has expired", map[bool]string{true: "up", false: "down"}[ok], false, nil)
	}
	j, _ := st.CreateJob(ctx, store.Job{Type: "update", DeviceID: &host.ID, DeviceName: "Unraid", Title: "Update Unraid"})
	st.FinishJob(ctx, j.ID, store.JobFailed, json.RawMessage(`{"error":"apt failed"}`))

	code, out := h.do(c, "GET", "/api/overview", nil)
	if code != 200 {
		t.Fatalf("overview: %d %v", code, out)
	}
	b, _ := json.Marshal(out)
	var ov struct {
		Problems []ovProblem `json:"problems"`
		Chores   ovChores    `json:"chores"`
		Today    ovToday     `json:"today"`
		Areas    []ovArea    `json:"areas"`
		Events   []ovEvent   `json:"events"`
	}
	json.Unmarshal(b, &ov)

	kinds := map[string]string{}
	for _, p := range ov.Problems {
		kinds[p.Kind+":"+p.Name] = p.Severity
	}
	for k, sev := range map[string]string{"monitor:Home Assistant": "crit", "offline:Proxmox": "crit", "warning:Unraid": "warn", "job:Update Unraid": "warn"} {
		if kinds[k] != sev {
			t.Errorf("problem %s = %q, want %s (all: %v)", k, kinds[k], sev, kinds)
		}
	}
	if ov.Problems[len(ov.Problems)-1].Severity != "warn" || ov.Problems[0].Severity != "crit" {
		t.Errorf("critical problems first: %+v", ov.Problems)
	}
	if ov.Chores.Inbox != 1 || len(ov.Chores.Reboot) != 1 {
		t.Errorf("chores: %+v", ov.Chores)
	}
	if ov.Today.Containers != (ovCount{0, 1}) || ov.Today.Devices != (ovCount{1, 3}) {
		t.Errorf("today: containers %+v devices %+v", ov.Today.Containers, ov.Today.Devices)
	}
	if len(ov.Areas) != 1 || ov.Areas[0].ID != AreaApps || ov.Areas[0].Health != "down" {
		t.Fatalf("areas: %+v", ov.Areas)
	}
	if got := ov.Areas[0].Ticks[25:]; got[0] != -1 || got[1] != 1 || got[4] != 0 {
		t.Errorf("ticks: %v", ov.Areas[0].Ticks)
	}
	var outage, failed bool
	for _, e := range ov.Events {
		outage = outage || e.Kind == "outage" && e.Until == nil
		failed = failed || e.Kind == "failed"
	}
	if !outage || !failed {
		t.Errorf("events: %+v", ov.Events)
	}
}

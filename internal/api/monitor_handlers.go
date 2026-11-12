package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/KrX3D/Homewarden/internal/auth"
	"github.com/KrX3D/Homewarden/internal/monitor"
	"github.com/KrX3D/Homewarden/internal/store"
)

// AttachMonitors registers monitors, maintenance windows, push and the status page.
func (s *Server) AttachMonitors(r *monitor.Runner) {
	s.monitors = r
	m := s.mux
	m.Handle("GET /api/monitors", s.authed(auth.PermDevicesView, s.listMonitors))
	m.Handle("POST /api/monitors", s.authed(auth.PermDevicesManage, s.saveMonitor))
	m.Handle("PUT /api/monitors/{id}", s.authed(auth.PermDevicesManage, s.saveMonitor))
	m.Handle("DELETE /api/monitors/{id}", s.authed(auth.PermDevicesManage, s.deleteMonitor))
	m.Handle("GET /api/monitors/{id}/results", s.authed(auth.PermDevicesView, s.monitorResults))
	m.Handle("POST /api/monitors/test", s.authed(auth.PermDevicesManage, s.testMonitor))
	m.Handle("GET /api/maintenance", s.authed(auth.PermDevicesView, s.listMaintenance))
	m.Handle("POST /api/maintenance", s.authed(auth.PermDevicesManage, s.addMaintenance))
	m.Handle("DELETE /api/maintenance/{id}", s.authed(auth.PermDevicesManage, s.deleteMaintenance))
	m.Handle("GET /api/settings/status-page", s.authed(auth.PermSettingsManage, s.getStatusPage))
	m.Handle("PUT /api/settings/status-page", s.authed(auth.PermSettingsManage, s.putStatusPage))
	m.HandleFunc("GET /api/push/{token}", s.push)
	m.HandleFunc("POST /api/push/{token}", s.push)
	m.HandleFunc("GET /api/public/status", s.publicStatus)
}

type monitorView struct {
	store.Monitor
	Uptime24h float64 `json:"uptime24h"`
	Uptime30d float64 `json:"uptime30d"`
}

func (s *Server) listMonitors(w http.ResponseWriter, r *http.Request) {
	mons, err := s.Store.Monitors(r.Context())
	if err != nil {
		internal(w, err)
		return
	}
	now := time.Now()
	out := []monitorView{}
	for _, m := range mons {
		u1, _ := s.Store.MonitorUptime(r.Context(), m.ID, now.Add(-24*time.Hour).Unix())
		u30, _ := s.Store.MonitorUptime(r.Context(), m.ID, now.AddDate(0, 0, -30).Unix())
		out = append(out, monitorView{m, u1, u30})
	}
	writeJSON(w, http.StatusOK, out)
}

type monitorIn struct {
	Name     string         `json:"name"`
	Type     string         `json:"type"`
	Config   monitor.Config `json:"config"`
	DeviceID *int64         `json:"deviceId"`
	ParentID *int64         `json:"parentId"`
	Interval int            `json:"interval"`
	Enabled  bool           `json:"enabled"`
	Public   bool           `json:"public"`
}

func (in *monitorIn) validate() string {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return "invalid_name"
	}
	if in.Interval < 20 {
		in.Interval = 60
	}
	in.Config.Tags = cleanTags(in.Config.Tags)
	if len(in.Config.Tags) == 0 {
		in.Config.Tags = nil
	}
	c := in.Config
	switch in.Type {
	case "http":
		if !strings.HasPrefix(c.URL, "http://") && !strings.HasPrefix(c.URL, "https://") {
			return "monitor_incomplete"
		}
	case "tcp":
		if c.Host == "" || c.Port <= 0 || c.Port > 65535 {
			return "monitor_incomplete"
		}
	case "ping", "dns":
		if c.Host == "" {
			return "monitor_incomplete"
		}
	case "container":
		if c.Container == "" && in.DeviceID == nil {
			return "monitor_incomplete"
		}
	case "mqtt", "postgres", "mysql", "redis":
		if c.Host == "" || c.Port < 0 || c.Port > 65535 {
			return "monitor_incomplete"
		}
	case "push":
	default:
		return "bad_request"
	}
	return ""
}

func newPushToken() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Server) saveMonitor(w http.ResponseWriter, r *http.Request) {
	var in monitorIn
	if !readJSON(w, r, &in) {
		return
	}
	if code := in.validate(); code != "" {
		fail(w, http.StatusBadRequest, code)
		return
	}
	cfg, _ := json.Marshal(in.Config)
	m := store.Monitor{Name: in.Name, Type: in.Type, Config: cfg, DeviceID: in.DeviceID, ParentID: in.ParentID, Interval: in.Interval, Enabled: in.Enabled, Public: in.Public}
	if r.Method == http.MethodPut {
		id, err := PathID(r)
		if err != nil {
			fail(w, http.StatusBadRequest, "bad_request")
			return
		}
		m.ID = id
	}
	if in.ParentID != nil && s.parentLoop(r, m.ID, *in.ParentID) {
		fail(w, http.StatusBadRequest, "monitor_parent_loop")
		return
	}
	if r.Method != http.MethodPut && in.Type == "push" {
		m.PushToken = newPushToken()
	}
	out, err := s.Store.SaveMonitor(r.Context(), m)
	if err != nil {
		internal(w, err)
		return
	}
	s.monitors.Poke()
	writeJSON(w, http.StatusOK, out)
}

// parentLoop reports a parent that is the monitor itself or one of its children.
func (s *Server) parentLoop(r *http.Request, id, parent int64) bool {
	seen := map[int64]bool{}
	for p := parent; ; {
		if p == id || seen[p] {
			return true
		}
		seen[p] = true
		m, err := s.Store.Monitor(r.Context(), p)
		if err != nil || m.ParentID == nil {
			return false
		}
		p = *m.ParentID
	}
}

func (s *Server) deleteMonitor(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	if err := s.Store.DeleteMonitor(r.Context(), id); err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) monitorResults(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	hours, _ := strconv.Atoi(r.URL.Query().Get("hours"))
	if hours <= 0 || hours > 24*30 {
		hours = 24
	}
	res, err := s.Store.MonitorResults(r.Context(), id, time.Now().Add(-time.Duration(hours)*time.Hour).Unix())
	if err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) testMonitor(w http.ResponseWriter, r *http.Request) {
	var in monitorIn
	if !readJSON(w, r, &in) {
		return
	}
	if code := in.validate(); code != "" || in.Type == "push" {
		fail(w, http.StatusBadRequest, "monitor_incomplete")
		return
	}
	res := s.monitors.Test(r.Context(), in.Type, in.Config, in.DeviceID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": res.OK, "ms": res.Ms, "msg": res.Msg, "certExpires": res.CertEnd})
}

// push records a heartbeat: GET or POST /api/push/<token>?status=up|down&msg=...
func (s *Server) push(w http.ResponseWriter, r *http.Request) {
	m, err := s.Store.MonitorByPushToken(r.Context(), r.PathValue("token"))
	if err != nil {
		fail(w, http.StatusNotFound, "not_found")
		return
	}
	q := r.URL.Query()
	ok := q.Get("status") != "down"
	msg := q.Get("msg")
	if msg == "" {
		msg = "push"
	}
	ms, _ := strconv.ParseFloat(q.Get("ping"), 64)
	s.monitors.Record(r.Context(), m, nil, monitor.Result{OK: ok, Ms: ms, Msg: msg})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) listMaintenance(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.MaintenanceWindows(r.Context())
	if err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) addMaintenance(w http.ResponseWriter, r *http.Request) {
	var in store.Maintenance
	if !readJSON(w, r, &in) {
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" || in.Ends <= in.Starts {
		fail(w, http.StatusBadRequest, "invalid_window")
		return
	}
	if err := s.Store.AddMaintenance(r.Context(), in); err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) deleteMaintenance(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	if err := s.Store.DeleteMaintenance(r.Context(), id); err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// StatusPage settings: the public page shows monitors marked public.
type StatusPage struct {
	Enabled bool   `json:"enabled"`
	Title   string `json:"title"`
}

func (s *Server) statusPage(r *http.Request) StatusPage {
	raw, _ := s.Store.GetSetting(r.Context(), "statusPage", "")
	sp := StatusPage{Title: "Status"}
	_ = json.Unmarshal([]byte(raw), &sp)
	return sp
}

func (s *Server) getStatusPage(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.statusPage(r))
}

func (s *Server) putStatusPage(w http.ResponseWriter, r *http.Request) {
	var in StatusPage
	if !readJSON(w, r, &in) {
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	b, _ := json.Marshal(in)
	if err := s.Store.SetSetting(r.Context(), "statusPage", string(b)); err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, in)
}

// publicStatus serves the status page data without login. It only contains monitors
// marked public, and no targets or messages that could reveal internals.
func (s *Server) publicStatus(w http.ResponseWriter, r *http.Request) {
	sp := s.statusPage(r)
	if !sp.Enabled {
		fail(w, http.StatusNotFound, "not_found")
		return
	}
	mons, err := s.Store.Monitors(r.Context())
	if err != nil {
		internal(w, err)
		return
	}
	type item struct {
		Name      string               `json:"name"`
		Status    string               `json:"status"`
		Uptime24h float64              `json:"uptime24h"`
		Uptime30d float64              `json:"uptime30d"`
		Bars      []store.MonitorPoint `json:"bars"`
	}
	now := time.Now()
	out := []item{}
	for _, m := range mons {
		if !m.Public || !m.Enabled {
			continue
		}
		u1, _ := s.Store.MonitorUptime(r.Context(), m.ID, now.Add(-24*time.Hour).Unix())
		u30, _ := s.Store.MonitorUptime(r.Context(), m.ID, now.AddDate(0, 0, -30).Unix())
		res, _ := s.Store.MonitorResults(r.Context(), m.ID, now.Add(-24*time.Hour).Unix())
		out = append(out, item{m.Name, m.Status, u1, u30, res})
	}
	maint, _ := s.Store.MaintenanceWindows(r.Context())
	active := []string{}
	for _, mw := range maint {
		if mw.Starts <= now.Unix() {
			active = append(active, mw.Title)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"title": sp.Title, "monitors": out, "maintenance": active})
}

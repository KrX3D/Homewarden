package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/KrX3D/Homewarden/internal/auth"
	"github.com/KrX3D/Homewarden/internal/discovery"
	"github.com/KrX3D/Homewarden/internal/events"
	"github.com/KrX3D/Homewarden/internal/oui"
	"github.com/KrX3D/Homewarden/internal/presence"
	"github.com/KrX3D/Homewarden/internal/store"
)

// DeviceDeps are wired in by main after construction.
type DeviceDeps struct {
	Events   *events.Broker
	Presence *presence.Engine
	Scanner  *discovery.Scanner
}

// AttachDevices registers device, presence, discovery and event routes.
func (s *Server) AttachDevices(d DeviceDeps) {
	s.dev = d
	m := s.mux
	m.Handle("GET /api/events", s.authed("", d.Events.ServeHTTP))

	m.Handle("GET /api/devices", s.authed(auth.PermDevicesView, s.listDevices))
	m.Handle("POST /api/devices", s.authed(auth.PermDevicesManage, s.createDevice))
	m.Handle("GET /api/devices/{id}", s.authed(auth.PermDevicesView, s.getDevice))
	m.Handle("PUT /api/devices/{id}", s.authed(auth.PermDevicesManage, s.updateDevice))
	m.Handle("DELETE /api/devices/{id}", s.authed(auth.PermDevicesManage, s.deleteDevice))
	m.Handle("POST /api/devices/{id}/status", s.authed(auth.PermDevicesManage, s.setDeviceStatus))
	m.Handle("POST /api/devices/{id}/check", s.authed(auth.PermDevicesView, s.checkDevice))
	m.Handle("GET /api/devices/{id}/presence", s.authed(auth.PermDevicesView, s.devicePresence))

	m.Handle("GET /api/discovery", s.authed(auth.PermDevicesManage, s.discoveryInfo))
	m.Handle("POST /api/discovery/scan", s.authed(auth.PermDevicesManage, s.discoveryScan))
}

type deviceInput struct {
	Name      string               `json:"name"`
	Kind      string               `json:"kind"`
	Host      string               `json:"host"`
	MAC       string               `json:"mac"`
	ParentID  *int64               `json:"parentId"`
	GroupName string               `json:"groupName"`
	Presence  store.PresenceConfig `json:"presence"`
	Config    json.RawMessage      `json:"config"`
	Notes     string               `json:"notes"`
}

func validProbe(p store.ProbeConfig, allowNone bool) bool {
	switch p.Method {
	case "none":
		return allowNone
	case "ping", "agent":
		return true
	case "tcp":
		return p.Port > 0 && p.Port < 65536
	case "http":
		return p.URL == "" || strings.HasPrefix(p.URL, "http://") || strings.HasPrefix(p.URL, "https://")
	}
	return false
}

func (in *deviceInput) validate() string {
	in.Name = strings.TrimSpace(in.Name)
	in.Host = strings.TrimSpace(in.Host)
	in.GroupName = strings.TrimSpace(in.GroupName)
	in.Presence.Normalize()
	switch {
	case in.Name == "" || len(in.Name) > 128:
		return "invalid_name"
	case len(in.Host) > 255 || strings.ContainsAny(in.Host, " /\\"):
		return "invalid_host"
	case in.Host == "" && in.Presence.Reach.Method != "none" && in.Presence.Reach.Method != "agent":
		return "host_required"
	case !validProbe(in.Presence.Reach, true) || !validProbe(in.Presence.Ready, true):
		return "invalid_probe"
	}
	if len(in.Config) > 0 && !json.Valid(in.Config) {
		return "bad_request"
	}
	return ""
}

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status == "" {
		status = store.StatusActive
	}
	if status == "all" {
		status = ""
	}
	devs, err := s.Store.Devices(r.Context(), status)
	if err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.visibleDevices(r, devs))
}

func (s *Server) createDevice(w http.ResponseWriter, r *http.Request) {
	var in deviceInput
	if !readJSON(w, r, &in) {
		return
	}
	if code := in.validate(); code != "" {
		fail(w, http.StatusBadRequest, code)
		return
	}
	d, err := s.Store.CreateDevice(r.Context(), store.Device{
		Name: in.Name, Kind: in.Kind, Host: in.Host, MAC: in.MAC, ParentID: in.ParentID, GroupName: in.GroupName,
		Presence: in.Presence, Config: in.Config, Notes: in.Notes,
	})
	if err != nil {
		internal(w, err)
		return
	}
	s.dev.Presence.Poke()
	s.dev.Events.Publish("device.changed", map[string]int64{"id": d.ID})
	writeJSON(w, http.StatusCreated, d)
}

func (s *Server) loadDevice(w http.ResponseWriter, r *http.Request) (store.Device, bool) {
	id, err := PathID(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request")
		return store.Device{}, false
	}
	d, err := s.Store.Device(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) || err == nil && !PrincipalFrom(r).SeesDevice(d) {
		fail(w, http.StatusNotFound, "not_found")
		return d, false
	}
	if err != nil {
		internal(w, err)
		return d, false
	}
	return d, true
}

func (s *Server) getDevice(w http.ResponseWriter, r *http.Request) {
	if d, ok := s.loadDevice(w, r); ok {
		d.Vendor = oui.Lookup(d.MAC)
		writeJSON(w, http.StatusOK, d)
	}
}

func (s *Server) updateDevice(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadDevice(w, r)
	if !ok {
		return
	}
	var in deviceInput
	if !readJSON(w, r, &in) {
		return
	}
	if code := in.validate(); code != "" {
		fail(w, http.StatusBadRequest, code)
		return
	}
	if in.ParentID != nil && *in.ParentID == d.ID {
		fail(w, http.StatusBadRequest, "invalid_parent")
		return
	}
	d.Name, d.Host, d.MAC, d.ParentID, d.GroupName, d.Presence, d.Notes = in.Name, in.Host, in.MAC, in.ParentID, in.GroupName, in.Presence, in.Notes
	if in.Kind != "" {
		d.Kind = in.Kind
	}
	if len(in.Config) > 0 {
		d.Config = in.Config
	}
	if err := s.Store.UpdateDevice(r.Context(), d); err != nil {
		internal(w, err)
		return
	}
	s.dev.Presence.Forget(d.ID)
	s.dev.Presence.Poke()
	s.dev.Events.Publish("device.changed", map[string]int64{"id": d.ID})
	out, _ := s.Store.Device(r.Context(), d.ID)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteDevice(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadDevice(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteDevice(r.Context(), d.ID); err != nil {
		internal(w, err)
		return
	}
	s.dev.Presence.Forget(d.ID)
	s.dev.Events.Publish("device.changed", map[string]int64{"id": d.ID})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// setDeviceStatus moves a device: add from the inbox (active), ignore, or restore an
// ignored one back to the inbox.
func (s *Server) setDeviceStatus(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadDevice(w, r)
	if !ok {
		return
	}
	var in struct{ Status string }
	if !readJSON(w, r, &in) {
		return
	}
	switch in.Status {
	case store.StatusActive, store.StatusDiscovered, store.StatusIgnored:
	default:
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	if err := s.Store.SetDeviceStatus(r.Context(), d.ID, in.Status); err != nil {
		internal(w, err)
		return
	}
	s.dev.Presence.Forget(d.ID)
	s.dev.Presence.Poke()
	s.dev.Events.Publish("device.changed", map[string]int64{"id": d.ID})
	out, _ := s.Store.Device(r.Context(), d.ID)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) checkDevice(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadDevice(w, r)
	if !ok {
		return
	}
	if d.Host == "" || d.Presence.Reach.Method == "none" {
		fail(w, http.StatusBadRequest, "no_presence_check")
		return
	}
	out, err := s.dev.Presence.CheckNow(r.Context(), d.ID)
	if err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) devicePresence(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadDevice(w, r)
	if !ok {
		return
	}
	hours, _ := strconv.Atoi(r.URL.Query().Get("hours"))
	if hours <= 0 || hours > 24*365 {
		hours = 24
	}
	since := time.Now().Add(-time.Duration(hours) * time.Hour).Unix()
	ev, err := s.Store.PresenceHistory(r.Context(), d.ID, since)
	if err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"since": since, "now": time.Now().Unix(), "events": ev})
}

func (s *Server) discoveryInfo(w http.ResponseWriter, r *http.Request) {
	cidr, err := s.Store.GetSetting(r.Context(), "discovery.cidr", "")
	if err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cidr": cidr, "running": s.dev.Scanner.Running(), "maxHosts": discovery.MaxHosts})
}

func (s *Server) discoveryScan(w http.ResponseWriter, r *http.Request) {
	var in struct{ CIDR string }
	if !readJSON(w, r, &in) {
		return
	}
	n, err := s.dev.Scanner.Start(in.CIDR)
	if errors.Is(err, discovery.ErrBusy) {
		fail(w, http.StatusConflict, "scan_running")
		return
	}
	if err != nil {
		FailMsg(w, http.StatusBadRequest, "invalid_cidr", err.Error())
		return
	}
	_ = s.Store.SetSetting(r.Context(), "discovery.cidr", strings.TrimSpace(in.CIDR))
	writeJSON(w, http.StatusAccepted, map[string]int{"hosts": n})
}

// visibleDevices filters devices by the principal's device groups and fills in the
// MAC vendor.
func (s *Server) visibleDevices(r *http.Request, devs []store.Device) []store.Device {
	p := PrincipalFrom(r)
	out := make([]store.Device, 0, len(devs))
	for _, d := range devs {
		if p.SeesDevice(d) {
			d.Vendor = oui.Lookup(d.MAC)
			out = append(out, d)
		}
	}
	return out
}

// visibleDeviceIDs returns the ids the principal may see, or nil for "all".
func (s *Server) visibleDeviceIDs(r *http.Request) map[int64]bool {
	if len(PrincipalFrom(r).Role.Groups) == 0 {
		return nil
	}
	devs, _ := s.Store.Devices(r.Context(), "")
	ids := map[int64]bool{}
	for _, d := range s.visibleDevices(r, devs) {
		ids[d.ID] = true
	}
	return ids
}

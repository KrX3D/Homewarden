package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/KrX3D/Homewarden/internal/auth"
	"github.com/KrX3D/Homewarden/internal/conn"
	"github.com/KrX3D/Homewarden/internal/integrations"
	"github.com/KrX3D/Homewarden/internal/jobs"
	"github.com/KrX3D/Homewarden/internal/store"
)

// SoftwareDeps are the integration and job services.
type SoftwareDeps struct {
	Registry *integrations.Registry
	Runner   *jobs.Runner
	Software *jobs.Software
}

// AttachSoftware registers integration, scan/update and job routes.
func (s *Server) AttachSoftware(d SoftwareDeps) {
	s.sw = d
	m := s.mux
	m.Handle("GET /api/integrations", s.authed(auth.PermDevicesView, s.listIntegrations))
	m.Handle("GET /api/integrations/catalog", s.authed(auth.PermIntegrationsManage, s.integrationCatalog))
	m.Handle("GET /api/integrations/review", s.authed(auth.PermIntegrationsManage, s.integrationReview))
	m.Handle("POST /api/integrations/install", s.authed(auth.PermIntegrationsManage, s.installIntegration))
	m.Handle("DELETE /api/integrations/{iid}", s.authed(auth.PermIntegrationsManage, s.uninstallIntegration))
	m.Handle("GET /api/integrations/{iid}/defaults", s.authed(auth.PermDevicesManage, s.getIntegrationDefaults))
	m.Handle("PUT /api/integrations/{iid}/defaults", s.authed(auth.PermIntegrationsManage, s.putIntegrationDefaults))
	m.Handle("GET /api/settings/catalog-sources", s.authed(auth.PermIntegrationsManage, s.getSources))
	m.Handle("PUT /api/settings/catalog-sources", s.authed(auth.PermIntegrationsManage, s.putSources))

	m.Handle("PUT /api/devices/{id}/integration", s.authed(auth.PermDevicesManage, s.setDeviceIntegration))
	m.Handle("GET /api/devices/{id}/software", s.authed(auth.PermDevicesView, s.deviceSoftware))
	m.Handle("GET /api/devices/{id}/network", s.authed(auth.PermDevicesView, s.deviceNetwork))
	m.Handle("POST /api/devices/{id}/scan", s.authed(auth.PermDevicesView, s.scanDevice))
	m.Handle("POST /api/devices/{id}/update", s.authed(auth.PermUpdatesRun, s.updateDevice2))
	m.Handle("GET /api/updates", s.authed(auth.PermDevicesView, s.pendingUpdates))
	m.Handle("GET /api/devices/{id}/metrics", s.authed(auth.PermDevicesView, s.deviceMetrics))
	m.Handle("GET /api/devices/{id}/changelog", s.authed(auth.PermDevicesView, s.componentChangelog))
	m.Handle("GET /api/devices/{id}/choices", s.authed(auth.PermUpdatesRun, s.componentChoices))
	m.Handle("POST /api/devices/{id}/metrics", s.authed(auth.PermDevicesView, s.sampleMetrics))

	m.Handle("GET /api/jobs", s.authed(auth.PermJobsView, s.listJobs))
	m.Handle("GET /api/jobs/{id}", s.authed(auth.PermJobsView, s.getJob))
	m.Handle("POST /api/jobs/{id}/cancel", s.authed(auth.PermUpdatesRun, s.cancelJob))
}

func (s *Server) listIntegrations(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.sw.Registry.All())
}

func (s *Server) sources(r *http.Request) []string {
	raw, _ := s.Store.GetSetting(r.Context(), "catalog.sources", "")
	if raw == "" {
		return []string{integrations.DefaultSource}
	}
	var out []string
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

func (s *Server) getSources(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"sources": s.sources(r), "default": integrations.DefaultSource})
}

func (s *Server) putSources(w http.ResponseWriter, r *http.Request) {
	var in struct{ Sources []string }
	if !readJSON(w, r, &in) {
		return
	}
	clean := []string{}
	for _, src := range in.Sources {
		src = strings.TrimSpace(src)
		if src == "" {
			continue
		}
		if !strings.HasPrefix(src, "https://") && !strings.HasPrefix(src, "http://") {
			fail(w, http.StatusBadRequest, "invalid_source")
			return
		}
		clean = append(clean, src)
	}
	b, _ := json.Marshal(clean)
	if err := s.Store.SetSetting(r.Context(), "catalog.sources", string(b)); err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sources": clean})
}

func (s *Server) integrationCatalog(w http.ResponseWriter, r *http.Request) {
	entries, errs := s.sw.Registry.Catalog(r.Context(), s.sources(r))
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "errors": errs})
}

// integrationReview returns the installed and the offered script so the user can see
// what changes before installing or updating.
func (s *Server) integrationReview(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	cand, _, err := s.sw.Registry.Candidate(r.Context(), q.Get("source"), q.Get("id"))
	if err != nil {
		FailMsg(w, http.StatusBadGateway, "catalog_unreachable", err.Error())
		return
	}
	out := map[string]any{"candidate": map[string]any{"version": cand.Version, "script": cand.Script(), "permissions": cand.Permissions, "manifest": cand.Manifest}}
	if cur, ok := s.sw.Registry.Get(cand.ID); ok {
		out["current"] = map[string]any{"version": cur.Version, "script": cur.Script(), "permissions": cur.Permissions, "source": cur.Source}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) installIntegration(w http.ResponseWriter, r *http.Request) {
	var in struct{ Source, ID string }
	if !readJSON(w, r, &in) {
		return
	}
	if in.Source != integrations.BuiltinSource {
		allowed := false
		for _, src := range s.sources(r) {
			if src == in.Source {
				allowed = true
			}
		}
		if !allowed {
			fail(w, http.StatusBadRequest, "invalid_source")
			return
		}
	}
	got, err := s.sw.Registry.Install(r.Context(), in.Source, in.ID)
	if err != nil {
		FailMsg(w, http.StatusBadGateway, "install_failed", err.Error())
		return
	}
	s.dev.Events.Publish("integrations.changed", map[string]string{"id": got.ID})
	writeJSON(w, http.StatusOK, got)
}

func (s *Server) uninstallIntegration(w http.ResponseWriter, r *http.Request) {
	err := s.sw.Registry.Uninstall(r.Context(), r.PathValue("iid"))
	if errors.Is(err, integrations.ErrInUse) {
		fail(w, http.StatusConflict, "integration_in_use")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	s.dev.Events.Publish("integrations.changed", map[string]string{"id": r.PathValue("iid")})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// getIntegrationDefaults returns the settings shared by all devices of an integration
// and the devices using it.
func (s *Server) getIntegrationDefaults(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("iid")
	if _, ok := s.sw.Registry.Get(id); !ok {
		fail(w, http.StatusNotFound, "unknown_integration")
		return
	}
	def, err := s.Store.IntegrationDefaults(r.Context(), id)
	if err != nil {
		internal(w, err)
		return
	}
	devs, err := s.Store.Devices(r.Context(), "")
	if err != nil {
		internal(w, err)
		return
	}
	type use struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		// Own lists the setting keys the device sets itself; OwnConnection is true when
		// it has its own credential (or uses the agent).
		Own           []string `json:"own"`
		OwnConnection bool     `json:"ownConnection"`
	}
	used := []use{}
	for _, d := range s.visibleDevices(r, devs) {
		if d.Kind != id || d.Status != store.StatusActive {
			continue
		}
		u := use{ID: d.ID, Name: d.Name, Own: []string{}}
		for k, v := range integrations.Settings(d) {
			if v != nil && v != "" {
				u.Own = append(u.Own, k)
			}
		}
		sort.Strings(u.Own)
		c := conn.FromDevice(d)
		u.OwnConnection = c.Method == "agent" || c.CredentialID != 0
		used = append(used, u)
	}
	if def.Connection == nil {
		def.Connection = map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": def.Settings, "connection": def.Connection, "devices": used})
}

// putIntegrationDefaults stores the shared settings. Only keys the manifest defines
// are kept.
func (s *Server) putIntegrationDefaults(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("iid")
	in, ok := s.sw.Registry.Get(id)
	if !ok {
		fail(w, http.StatusNotFound, "unknown_integration")
		return
	}
	var body store.IntegrationDefaults
	if !readJSON(w, r, &body) {
		return
	}
	out := store.IntegrationDefaults{Settings: map[string]any{}}
	for _, st := range in.Settings {
		if st.Scope == "device" {
			continue
		}
		if v, ok := body.Settings[st.Key]; ok && v != nil && v != "" {
			out.Settings[st.Key] = v
		}
	}
	if in.Connection == "ssh" || in.Permissions.SSH {
		if cid, _ := body.Connection["credentialId"].(float64); cid != 0 {
			if _, err := s.Store.Credential(r.Context(), int64(cid)); err != nil {
				fail(w, http.StatusBadRequest, "unknown_credential")
				return
			}
			out.Connection = map[string]any{"credentialId": cid}
			if u, _ := body.Connection["user"].(string); strings.TrimSpace(u) != "" {
				out.Connection["user"] = strings.TrimSpace(u)
			}
			port, _ := body.Connection["port"].(float64)
			if port < 1 || port > 65535 {
				port = 22
			}
			out.Connection["port"] = port
			sudo, _ := body.Connection["sudo"].(bool)
			out.Connection["sudo"] = sudo
		}
	}
	if err := s.Store.SetIntegrationDefaults(r.Context(), id, out); err != nil {
		internal(w, err)
		return
	}
	s.dev.Events.Publish("integrations.changed", map[string]string{"id": id})
	writeJSON(w, http.StatusOK, out)
}

// setDeviceIntegration picks the integration (kind) and stores its settings.
func (s *Server) setDeviceIntegration(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadDevice(w, r)
	if !ok {
		return
	}
	var in struct {
		Kind     string         `json:"kind"`
		Settings map[string]any `json:"settings"`
		// ApplyPresence also takes over the integration's online detection, used when
		// a device is added from the inbox straight as this integration.
		ApplyPresence bool `json:"applyPresence"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.Kind != "generic" {
		integ, ok := s.sw.Registry.Get(in.Kind)
		if !ok {
			fail(w, http.StatusBadRequest, "unknown_integration")
			return
		}
		if in.ApplyPresence && integ.Presence != nil {
			d.Presence = *integ.Presence
		}
	}
	cfg := map[string]json.RawMessage{}
	_ = json.Unmarshal(d.Config, &cfg)
	sb, _ := json.Marshal(in.Settings)
	cfg["settings"] = sb
	b, _ := json.Marshal(cfg)
	d.Kind, d.Config = in.Kind, b
	if err := s.Store.UpdateDevice(r.Context(), d); err != nil {
		internal(w, err)
		return
	}
	s.dev.Events.Publish("device.changed", map[string]int64{"id": d.ID})
	out, _ := s.Store.Device(r.Context(), d.ID)
	writeJSON(w, http.StatusOK, out)
}

// deviceNetwork returns what other devices (a UniFi console) report about this one.
func (s *Server) deviceNetwork(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadDevice(w, r)
	if !ok {
		return
	}
	out, err := s.Store.NetworkInfoFor(r.Context(), d.ID)
	if err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deviceSoftware(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadDevice(w, r)
	if !ok {
		return
	}
	comps, err := s.Store.Components(r.Context(), d.ID)
	if err != nil {
		internal(w, err)
		return
	}
	facts, _ := s.Store.DeviceFacts(r.Context(), d.ID)
	hist, _ := s.Store.VersionHistory(r.Context(), d.ID, 50)
	recent, _ := s.Store.Jobs(r.Context(), d.ID, 10)
	writeJSON(w, http.StatusOK, map[string]any{"components": comps, "facts": facts, "history": hist, "jobs": recent})
}

func (s *Server) scanDevice(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadDevice(w, r)
	if !ok {
		return
	}
	if _, ok := s.sw.Registry.Get(d.Kind); !ok {
		fail(w, http.StatusBadRequest, "no_integration")
		return
	}
	j, err := s.sw.Software.EnqueueScan(r.Context(), d, PrincipalFrom(r).User.Username)
	if err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, j)
}

func (s *Server) updateDevice2(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadDevice(w, r)
	if !ok {
		return
	}
	var p jobs.UpdateParams
	if !readJSON(w, r, &p) {
		return
	}
	if len(p.Components) == 0 {
		fail(w, http.StatusBadRequest, "nothing_selected")
		return
	}
	if _, ok := s.sw.Registry.Get(d.Kind); !ok {
		fail(w, http.StatusBadRequest, "no_integration")
		return
	}
	b, _ := json.Marshal(p)
	title := "Update " + d.Name
	if len(p.Components) == 1 && p.Components[0] != "*" {
		title += ": " + strings.TrimPrefix(p.Components[0], "pkg:")
	} else if p.Components[0] == "*" {
		title += ": everything"
	} else {
		title += ": " + strconv.Itoa(len(p.Components)) + " components"
	}
	id := d.ID
	j, err := s.sw.Runner.Enqueue(r.Context(), store.Job{Type: "update", DeviceID: &id, DeviceName: d.Name, Title: title, Params: b, CreatedBy: PrincipalFrom(r).User.Username})
	if err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, j)
}

func (s *Server) pendingUpdates(w http.ResponseWriter, r *http.Request) {
	comps, err := s.Store.PendingUpdates(r.Context())
	if err != nil {
		internal(w, err)
		return
	}
	devs, err := s.Store.Devices(r.Context(), store.StatusActive)
	if err != nil {
		internal(w, err)
		return
	}
	if ids := s.visibleDeviceIDs(r); ids != nil {
		keep := comps[:0]
		for _, c := range comps {
			if ids[c.DeviceID] {
				keep = append(keep, c)
			}
		}
		comps = keep
	}
	writeJSON(w, http.StatusOK, map[string]any{"components": comps, "devices": s.visibleDevices(r, devs)})
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	dev, _ := strconv.ParseInt(r.URL.Query().Get("device"), 10, 64)
	js, err := s.Store.Jobs(r.Context(), dev, 200)
	if err != nil {
		internal(w, err)
		return
	}
	if ids := s.visibleDeviceIDs(r); ids != nil {
		keep := js[:0]
		for _, j := range js {
			if j.DeviceID != nil && ids[*j.DeviceID] {
				keep = append(keep, j)
			}
		}
		js = keep
	}
	writeJSON(w, http.StatusOK, js)
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	j, err := s.Store.Job(r.Context(), id)
	if ids := s.visibleDeviceIDs(r); err == nil && ids != nil && (j.DeviceID == nil || !ids[*j.DeviceID]) {
		err = store.ErrNotFound
	}
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, j)
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"cancelled": s.sw.Runner.Cancel(id)})
}

func (s *Server) deviceMetrics(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadDevice(w, r)
	if !ok {
		return
	}
	hours, _ := strconv.Atoi(r.URL.Query().Get("hours"))
	if hours <= 0 || hours > 24*30 {
		hours = 24
	}
	pts, err := s.Store.Metrics(r.Context(), d.ID, time.Now().Add(-time.Duration(hours)*time.Hour).Unix())
	if err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pts)
}

// sampleMetrics takes a sample now, including SMART.
func (s *Server) sampleMetrics(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadDevice(w, r)
	if !ok {
		return
	}
	if err := s.sw.Software.SampleDevice(r.Context(), d, true); err != nil {
		FailMsg(w, http.StatusBadGateway, "metrics_failed", err.Error())
		return
	}
	p, _ := s.Store.LatestMetrics(r.Context(), d.ID)
	writeJSON(w, http.StatusOK, p)
}

// callComponent runs an integration function that takes a component id.
func (s *Server) callComponent(w http.ResponseWriter, r *http.Request, capability, fn string) {
	d, ok := s.loadDevice(w, r)
	if !ok {
		return
	}
	in, ok := s.sw.Registry.Get(d.Kind)
	if !ok || !in.Has(capability) {
		fail(w, http.StatusBadRequest, "not_supported")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	raw, err := s.sw.Software.Host.Call(ctx, in, d, fn, r.URL.Query().Get("component"), nil)
	if err != nil {
		FailMsg(w, http.StatusBadGateway, "integration_failed", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw)
}

// componentChangelog returns {text, url} for one component.
func (s *Server) componentChangelog(w http.ResponseWriter, r *http.Request) {
	s.callComponent(w, r, "changelog", "changelog")
}

// componentChoices returns the versions a component can be switched to (rollback or
// another tag). Pick one by updating "<component>@<choice>".
func (s *Server) componentChoices(w http.ResponseWriter, r *http.Request) {
	s.callComponent(w, r, "choose", "choices")
}

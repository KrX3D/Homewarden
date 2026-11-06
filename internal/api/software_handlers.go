package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/KrX3D/Homewarden/internal/auth"
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
	m.Handle("GET /api/settings/catalog-sources", s.authed(auth.PermIntegrationsManage, s.getSources))
	m.Handle("PUT /api/settings/catalog-sources", s.authed(auth.PermIntegrationsManage, s.putSources))

	m.Handle("PUT /api/devices/{id}/integration", s.authed(auth.PermDevicesManage, s.setDeviceIntegration))
	m.Handle("GET /api/devices/{id}/software", s.authed(auth.PermDevicesView, s.deviceSoftware))
	m.Handle("POST /api/devices/{id}/scan", s.authed(auth.PermDevicesView, s.scanDevice))
	m.Handle("POST /api/devices/{id}/update", s.authed(auth.PermUpdatesRun, s.updateDevice2))
	m.Handle("GET /api/updates", s.authed(auth.PermDevicesView, s.pendingUpdates))

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

// setDeviceIntegration picks the integration (kind) and stores its settings.
func (s *Server) setDeviceIntegration(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadDevice(w, r)
	if !ok {
		return
	}
	var in struct {
		Kind     string         `json:"kind"`
		Settings map[string]any `json:"settings"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.Kind != "generic" {
		if _, ok := s.sw.Registry.Get(in.Kind); !ok {
			fail(w, http.StatusBadRequest, "unknown_integration")
			return
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
	writeJSON(w, http.StatusOK, map[string]any{"components": comps, "devices": devs})
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	dev, _ := strconv.ParseInt(r.URL.Query().Get("device"), 10, 64)
	js, err := s.Store.Jobs(r.Context(), dev, 200)
	if err != nil {
		internal(w, err)
		return
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

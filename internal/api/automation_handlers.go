package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/KrX3D/Homewarden/internal/auth"
	"github.com/KrX3D/Homewarden/internal/automation"
	"github.com/KrX3D/Homewarden/internal/jobs"
)

// AttachAutomation registers API tokens, update policies, pending actions and MQTT.
func (s *Server) AttachAutomation(a *automation.Service, m *automation.MQTT) {
	s.auto, s.mqtt = a, m
	mux := s.mux
	mux.Handle("GET /api/me/tokens", s.authed("", s.listTokens))
	mux.Handle("POST /api/me/tokens", s.authed("", s.createToken))
	mux.Handle("DELETE /api/me/tokens/{id}", s.authed("", s.deleteToken))
	mux.Handle("PUT /api/devices/{id}/policy", s.authed(auth.PermDevicesManage, s.setPolicy))
	mux.Handle("GET /api/devices/{id}/pending", s.authed(auth.PermDevicesView, s.devicePending))
	mux.Handle("POST /api/devices/{id}/trigger", s.authed(auth.PermUpdatesRun, s.triggerDevice))
	mux.Handle("GET /api/settings/mqtt", s.authed(auth.PermSettingsManage, s.getMQTT))
	mux.Handle("GET /api/settings/api", s.authed(auth.PermSettingsManage, s.getAPISettings))
	mux.Handle("PUT /api/settings/api", s.authed(auth.PermSettingsManage, s.putAPISettings))
	mux.Handle("PUT /api/settings/mqtt", s.authed(auth.PermSettingsManage, s.putMQTT))
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	t, err := s.Store.APITokens(r.Context(), PrincipalFrom(r).User.ID)
	if err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     string
		ReadOnly bool
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 64 {
		fail(w, http.StatusBadRequest, "invalid_name")
		return
	}
	raw, _ := auth.NewToken()
	token := "hw_" + raw
	t, err := s.Store.CreateAPIToken(r.Context(), PrincipalFrom(r).User.ID, in.Name, auth.HashToken(token), in.ReadOnly)
	if err != nil {
		internal(w, err)
		return
	}
	// The token itself is only ever shown in this response.
	writeJSON(w, http.StatusCreated, map[string]any{"token": token, "info": t})
}

func (s *Server) getAPISettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": s.Store.APIEnabled(r.Context())})
}

// putAPISettings turns token access on or off for everyone. Sessions (the web
// interface), agents and push monitors are not affected.
func (s *Server) putAPISettings(w http.ResponseWriter, r *http.Request) {
	var in struct{ Enabled bool }
	if !readJSON(w, r, &in) {
		return
	}
	v := "0"
	if in.Enabled {
		v = "1"
	}
	if err := s.Store.SetSetting(r.Context(), "api.enabled", v); err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": in.Enabled})
}

func (s *Server) deleteToken(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	if err := s.Store.DeleteAPIToken(r.Context(), PrincipalFrom(r).User.ID, id); err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) setPolicy(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadDevice(w, r)
	if !ok {
		return
	}
	var in jobs.Policy
	if !readJSON(w, r, &in) {
		return
	}
	in.Schedule = strings.TrimSpace(in.Schedule)
	switch in.Mode {
	case "manual", "auto":
	case "scheduled":
		if !automation.ValidCron(in.Schedule) {
			fail(w, http.StatusBadRequest, "invalid_cron")
			return
		}
	default:
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	if in.Scope != "all" {
		in.Scope = "security"
	}
	s.setConfigKey(w, r, d, "policy", in)
}

func (s *Server) devicePending(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadDevice(w, r)
	if !ok {
		return
	}
	p, err := s.Store.Pending(r.Context(), d.ID)
	if err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// triggerDevice is the simple endpoint for scripts and Home Assistant:
// {"action": "scan" | "update" | "backup", "scope": "security" | "all"}. When the
// device is offline the action waits until it is back.
func (s *Server) triggerDevice(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadDevice(w, r)
	if !ok {
		return
	}
	var in struct{ Action, Scope string }
	if !readJSON(w, r, &in) {
		return
	}
	if in.Action != "scan" && in.Action != "update" && in.Action != "backup" {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	if in.Scope != "all" {
		in.Scope = "security"
	}
	res, err := s.auto.Trigger(r.Context(), d, in.Action, in.Scope, PrincipalFrom(r).User.Username)
	if err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"result": res})
}

func (s *Server) getMQTT(w http.ResponseWriter, r *http.Request) {
	cfg := automation.LoadSettings(r.Context(), s.Store)
	writeJSON(w, http.StatusOK, map[string]any{"settings": cfg, "status": s.mqtt.Status(), "commandTopic": automation.Describe(cfg)})
}

func (s *Server) putMQTT(w http.ResponseWriter, r *http.Request) {
	var in automation.MQTTSettings
	if !readJSON(w, r, &in) {
		return
	}
	in.Broker = strings.TrimSpace(in.Broker)
	if in.Enabled && !strings.HasPrefix(in.Broker, "tcp://") && !strings.HasPrefix(in.Broker, "ssl://") && !strings.HasPrefix(in.Broker, "ws://") {
		fail(w, http.StatusBadRequest, "invalid_broker")
		return
	}
	in.BaseTopic = strings.Trim(strings.TrimSpace(in.BaseTopic), "/")
	if in.BaseTopic == "" {
		in.BaseTopic = "homewarden"
	}
	b, _ := json.Marshal(in)
	if err := s.Store.SetSetting(r.Context(), "mqtt", string(b)); err != nil {
		internal(w, err)
		return
	}
	// The connection must outlive this request.
	s.mqtt.Reload(s.baseCtx)
	writeJSON(w, http.StatusOK, map[string]any{"settings": in, "status": s.mqtt.Status()})
}

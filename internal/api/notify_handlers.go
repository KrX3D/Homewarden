package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/KrX3D/Homewarden/internal/auth"
	"github.com/KrX3D/Homewarden/internal/notify"
	"github.com/KrX3D/Homewarden/internal/store"
)

// AttachNotify registers notification channels and settings.
func (s *Server) AttachNotify(n *notify.Service) {
	s.notify = n
	m := s.mux
	m.Handle("GET /api/notify", s.authed(auth.PermSettingsManage, s.getNotify))
	m.Handle("PUT /api/notify/settings", s.authed(auth.PermSettingsManage, s.putNotifySettings))
	m.Handle("POST /api/notify/channels", s.authed(auth.PermSettingsManage, s.saveChannel))
	m.Handle("PUT /api/notify/channels/{id}", s.authed(auth.PermSettingsManage, s.saveChannel))
	m.Handle("DELETE /api/notify/channels/{id}", s.authed(auth.PermSettingsManage, s.deleteChannel))
	m.Handle("POST /api/notify/test", s.authed(auth.PermSettingsManage, s.testChannel))
	m.Handle("PUT /api/devices/{id}/notify", s.authed(auth.PermDevicesManage, s.setDeviceNotify))
}

type channelOut struct {
	store.NotifyChannel
	Summary string `json:"summary"`
}

func summarize(typ string, c notify.Config) string {
	switch typ {
	case "ntfy":
		return strings.TrimRight(c.URL, "/") + "/" + c.Topic
	case "gotify":
		return c.URL
	case "telegram":
		return "chat " + c.ChatID
	case "email":
		return c.To
	case "discord", "webhook":
		if i := strings.Index(c.URL, "://"); i > 0 {
			host := c.URL[i+3:]
			if j := strings.Index(host, "/"); j > 0 {
				host = host[:j]
			}
			return host
		}
	}
	return ""
}

func (s *Server) getNotify(w http.ResponseWriter, r *http.Request) {
	chans, err := s.Store.NotifyChannels(r.Context())
	if err != nil {
		internal(w, err)
		return
	}
	out := []channelOut{}
	for _, c := range chans {
		cfg, _ := s.notify.DecryptConfig(c)
		out = append(out, channelOut{c, summarize(c.Type, cfg)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": out, "settings": notify.LoadSettings(r.Context(), s.Store), "events": notify.Events})
}

func (s *Server) putNotifySettings(w http.ResponseWriter, r *http.Request) {
	var in notify.Settings
	if !readJSON(w, r, &in) {
		return
	}
	in.BaseURL = strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
	b, _ := json.Marshal(in)
	if err := s.Store.SetSetting(r.Context(), "notify", string(b)); err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, in)
}

type channelIn struct {
	Name    string        `json:"name"`
	Type    string        `json:"type"`
	Config  notify.Config `json:"config"`
	Events  []string      `json:"events"`
	Tags    []string      `json:"tags"`
	Enabled bool          `json:"enabled"`
	// Keep reuses the stored secrets when the form left them empty.
	Keep bool `json:"keep"`
}

// cleanTags trims, lowercases and de-duplicates tags.
func cleanTags(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, t := range in {
		t = strings.ToLower(strings.TrimSpace(t))
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

func validChannel(in *channelIn) string {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return "invalid_name"
	}
	c := in.Config
	switch in.Type {
	case "discord", "webhook":
		if !strings.HasPrefix(c.URL, "https://") && !strings.HasPrefix(c.URL, "http://") {
			return "channel_incomplete"
		}
	case "ntfy":
		if c.Topic == "" {
			return "channel_incomplete"
		}
	case "gotify":
		if c.URL == "" || c.Token == "" {
			return "channel_incomplete"
		}
	case "telegram":
		if c.Token == "" || c.ChatID == "" {
			return "channel_incomplete"
		}
	case "email":
		if c.Host == "" || c.From == "" || c.To == "" {
			return "channel_incomplete"
		}
	default:
		return "bad_request"
	}
	return ""
}

func (s *Server) saveChannel(w http.ResponseWriter, r *http.Request) {
	var in channelIn
	if !readJSON(w, r, &in) {
		return
	}
	var id int64
	if r.Method == http.MethodPut {
		var err error
		if id, err = PathID(r); err != nil {
			fail(w, http.StatusBadRequest, "bad_request")
			return
		}
		// Empty secret fields mean "unchanged".
		if old, err := s.Store.NotifyChannel(r.Context(), id); err == nil {
			if prev, err := s.notify.DecryptConfig(old); err == nil {
				if in.Config.URL == "" {
					in.Config.URL = prev.URL
				}
				if in.Config.Token == "" {
					in.Config.Token = prev.Token
				}
				if in.Config.Password == "" {
					in.Config.Password = prev.Password
				}
			}
		}
	}
	if code := validChannel(&in); code != "" {
		fail(w, http.StatusBadRequest, code)
		return
	}
	for _, e := range in.Events {
		valid := false
		for _, x := range notify.Events {
			valid = valid || x == e
		}
		if !valid {
			fail(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	b, _ := json.Marshal(in.Config)
	enc, err := s.Vault.Encrypt(b)
	if err != nil {
		internal(w, err)
		return
	}
	out, err := s.Store.SaveNotifyChannel(r.Context(), store.NotifyChannel{ID: id, Name: in.Name, Type: in.Type, Config: enc, Events: in.Events, Tags: cleanTags(in.Tags), Enabled: in.Enabled})
	if err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteChannel(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	if err := s.Store.DeleteNotifyChannel(r.Context(), id); err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// testChannel sends a test message with the form's values, or a saved channel's
// ({"id": n}).
func (s *Server) testChannel(w http.ResponseWriter, r *http.Request) {
	var in struct {
		channelIn
		ID int64 `json:"id"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	cfg := in.Config
	typ := in.Type
	if in.ID != 0 {
		c, err := s.Store.NotifyChannel(r.Context(), in.ID)
		if err != nil {
			fail(w, http.StatusNotFound, "not_found")
			return
		}
		prev, err := s.notify.DecryptConfig(c)
		if err != nil {
			internal(w, err)
			return
		}
		if typ == "" {
			typ = c.Type
		}
		if cfg.URL == "" {
			cfg.URL = prev.URL
		}
		if cfg.Token == "" {
			cfg.Token = prev.Token
		}
		if cfg.Password == "" {
			cfg.Password = prev.Password
		}
		if cfg.Topic == "" && cfg.ChatID == "" && cfg.Host == "" {
			cfg = prev
		}
	}
	set := notify.LoadSettings(r.Context(), s.Store)
	title, body := "Homewarden test", "Notifications from Homewarden reach you here."
	if set.Lang == "de" {
		title, body = "Homewarden-Test", "Benachrichtigungen von Homewarden kommen hier an."
	}
	if err := notify.Send(r.Context(), typ, cfg, notify.Message{Title: title, Body: body, Event: "test"}); err != nil {
		FailMsg(w, http.StatusBadGateway, "notify_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) setDeviceNotify(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadDevice(w, r)
	if !ok {
		return
	}
	var in struct {
		OfflineMinutes int `json:"offlineMinutes"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.OfflineMinutes < 0 || in.OfflineMinutes > 10080 {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.setConfigKey(w, r, d, "notify", in)
}

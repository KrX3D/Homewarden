package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/KrX3D/Homewarden/internal/store"
)

// Watching Homewarden from outside, for when it (or the NAS it runs on) is down and
// can no longer send anything itself:
//   - Heartbeat: Homewarden calls a URL every few minutes (healthchecks.io, Uptime Kuma
//     push, ntfy...). The outside service alerts when the calls stop.
//   - Agent watch: chosen agents get a notification channel with their poll answer.
//     When they cannot reach Homewarden for a while, they send the alert themselves.

// Heartbeat is setting watch.heartbeat.
type Heartbeat struct {
	URL     string `json:"url"`
	Minutes int    `json:"minutes"`
}

// AgentWatch is setting watch.agents.
type AgentWatch struct {
	DeviceIDs []int64 `json:"deviceIds"`
	ChannelID int64   `json:"channelId"`
	Minutes   int     `json:"minutes"`
	Host      string  `json:"host"` // optional host:port the agent also tests (the NAS)
}

// LoadHeartbeat reads the heartbeat setting (Minutes defaults to 5).
func LoadHeartbeat(ctx context.Context, st *store.Store) Heartbeat {
	raw, _ := st.GetSetting(ctx, "watch.heartbeat", "")
	var h Heartbeat
	_ = json.Unmarshal([]byte(raw), &h)
	if h.Minutes <= 0 {
		h.Minutes = 5
	}
	return h
}

// LoadAgentWatch reads the agent watch setting (Minutes defaults to 5).
func LoadAgentWatch(ctx context.Context, st *store.Store) AgentWatch {
	raw, _ := st.GetSetting(ctx, "watch.agents", "")
	w := AgentWatch{DeviceIDs: []int64{}}
	_ = json.Unmarshal([]byte(raw), &w)
	if w.Minutes <= 0 {
		w.Minutes = 5
	}
	if w.DeviceIDs == nil {
		w.DeviceIDs = []int64{}
	}
	return w
}

// HeartbeatStatus is the outcome of the last heartbeat call.
type HeartbeatStatus struct {
	At    int64  `json:"at"`
	Error string `json:"error"`
}

// Heartbeat returns the last heartbeat outcome.
func (s *Service) Heartbeat() HeartbeatStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hb
}

// PingHeartbeat calls the heartbeat URL now.
func (s *Service) PingHeartbeat(ctx context.Context, url string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err == nil {
		req.Header.Set("User-Agent", "Homewarden-Heartbeat")
		var resp *http.Response
		if resp, err = http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 300 {
				err = fmt.Errorf("HTTP %d", resp.StatusCode)
			}
		}
	}
	st := HeartbeatStatus{At: time.Now().Unix()}
	if err != nil {
		st.Error = err.Error()
	}
	s.mu.Lock()
	s.hb = st
	s.mu.Unlock()
	return err
}

// HeartbeatLoop calls the heartbeat URL on its interval until ctx ends.
func (s *Service) HeartbeatLoop(ctx context.Context) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	var last time.Time
	for {
		h := LoadHeartbeat(ctx, s.St)
		if h.URL != "" && time.Since(last) >= time.Duration(h.Minutes)*time.Minute {
			last = time.Now()
			_ = s.PingHeartbeat(ctx, h.URL)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// AgentWatchConfig is what a watching agent receives with each poll answer. Texts
// carry {agent}, {server}, {since} and {host} placeholders the agent fills in.
type AgentWatchConfig struct {
	Type    string            `json:"type"`
	Config  Config            `json:"config"`
	Minutes int               `json:"minutes"`
	Host    string            `json:"host,omitempty"`
	Texts   map[string]string `json:"texts"`
}

// WatchFor returns the watch config for an agent's device, nil when it does not watch.
func (s *Service) WatchFor(ctx context.Context, deviceID int64) *AgentWatchConfig {
	w := LoadAgentWatch(ctx, s.St)
	if w.ChannelID == 0 || !slices.Contains(w.DeviceIDs, deviceID) {
		return nil
	}
	ch, err := s.St.NotifyChannel(ctx, w.ChannelID)
	if err != nil {
		return nil
	}
	cfg, err := s.DecryptConfig(ch)
	if err != nil {
		return nil
	}
	set := LoadSettings(ctx, s.St)
	t := func(en, de string) string { return s.text(set.Lang, en, de) }
	return &AgentWatchConfig{Type: ch.Type, Config: cfg, Minutes: w.Minutes, Host: w.Host, Texts: map[string]string{
		"downTitle": t("Homewarden is unreachable", "Homewarden ist nicht erreichbar"),
		"downBody":  t("{agent} has not reached Homewarden at {server} since {since}.", "{agent} erreicht Homewarden unter {server} seit {since} nicht."),
		"hostUp":    t("{host} answers, so Homewarden itself (or its container) is down.", "{host} antwortet, also ist Homewarden selbst (oder sein Container) ausgefallen."),
		"hostDown":  t("{host} does not answer either.", "{host} antwortet auch nicht."),
		"upTitle":   t("Homewarden is reachable again", "Homewarden ist wieder erreichbar"),
		"upBody":    t("{agent} reaches Homewarden again (unreachable since {since}).", "{agent} erreicht Homewarden wieder (nicht erreichbar seit {since})."),
	}}
}

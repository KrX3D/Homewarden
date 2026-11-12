package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/KrX3D/Homewarden/internal/auth"
	"github.com/KrX3D/Homewarden/internal/store"
)

// A read-only MCP endpoint (Model Context Protocol, streamable HTTP, JSON answers only)
// so an AI assistant can ask "what is wrong in the homelab". Off by default (setting
// mcp.enabled); authenticated with an API token like the rest of the API. It only
// reads and never returns secrets or configs.

var mcpVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26"}

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

var mcpTools = []mcpTool{
	{"overview", "What is wrong now (down monitors, offline devices, warnings, failed jobs), what waits for the user (updates, new devices, certificates), today's figures, each area's health and the last seven days.", noArgs()},
	{"devices", "All devices Homewarden manages: name, kind, address, state (ready, reachable, offline), parent, pending updates.", noArgs()},
	{"monitors", "All uptime monitors: name, type, status (up, down, dependency, unknown), last message, response time, uptime 24 h and 30 d, certificate expiry.", noArgs()},
	{"updates", "Pending updates per device: component, installed and available version, security flag.", noArgs()},
}

func noArgs() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
}

// AttachMCP registers POST /api/mcp. Call after AttachOverview.
func (s *Server) AttachMCP() {
	s.mux.Handle("POST /api/mcp", s.authed(auth.PermDevicesView, s.mcp))
	s.mux.HandleFunc("GET /api/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
	})
	s.mux.Handle("GET /api/settings/mcp", s.authed(auth.PermSettingsManage, s.getMCPSettings))
	s.mux.Handle("PUT /api/settings/mcp", s.authed(auth.PermSettingsManage, s.putMCPSettings))
}

func (s *Server) mcpEnabled(r *http.Request) bool {
	v, _ := s.Store.GetSetting(r.Context(), "mcp.enabled", "0")
	return v == "1"
}

func (s *Server) getMCPSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": s.mcpEnabled(r)})
}

func (s *Server) putMCPSettings(w http.ResponseWriter, r *http.Request) {
	var in struct{ Enabled bool }
	if !readJSON(w, r, &in) {
		return
	}
	v := "0"
	if in.Enabled {
		v = "1"
	}
	if err := s.Store.SetSetting(r.Context(), "mcp.enabled", v); err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": in.Enabled})
}

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s *Server) mcp(w http.ResponseWriter, r *http.Request) {
	if !s.mcpEnabled(r) {
		fail(w, http.StatusNotFound, "mcp_disabled")
		return
	}
	// Browsers send Origin; only the page's own origin may call (DNS rebinding guard).
	if o := r.Header.Get("Origin"); o != "" {
		if u, err := url.Parse(o); err != nil || u.Host != r.Host {
			fail(w, http.StatusForbidden, "origin")
			return
		}
	}
	var req rpcReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"jsonrpc": "2.0", "id": nil, "error": rpcErr{-32700, "parse error"}})
		return
	}
	if len(req.ID) == 0 { // notification (e.g. notifications/initialized)
		w.WriteHeader(http.StatusAccepted)
		return
	}
	result, rerr := s.mcpCall(r, req)
	out := map[string]any{"jsonrpc": "2.0", "id": req.ID}
	if rerr != nil {
		out["error"] = rerr
	} else {
		out["result"] = result
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) mcpCall(r *http.Request, req rpcReq) (any, *rpcErr) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := mcpVersions[0]
		for _, v := range mcpVersions {
			if v == p.ProtocolVersion {
				version = v
			}
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "homewarden", "version": s.Build.Version},
			"instructions":    "Homewarden watches a homelab: devices, their updates, uptime monitors and backups. Start with the overview tool.",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": mcpTools}, nil
	case "tools/call":
		var p struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(req.Params, &p)
		data, err := s.mcpTool(r, p.Name)
		if err != nil {
			return map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": err.Error()}}}, nil
		}
		if data == nil {
			return nil, &rpcErr{-32602, "unknown tool " + p.Name}
		}
		b, _ := json.Marshal(data)
		return map[string]any{"content": []map[string]any{{"type": "text", "text": string(b)}}}, nil
	}
	return nil, &rpcErr{-32601, "method not found: " + req.Method}
}

func (s *Server) mcpTool(r *http.Request, name string) (any, error) {
	ctx := r.Context()
	p := PrincipalFrom(r)
	switch name {
	case "overview":
		return s.buildOverview(r)
	case "devices":
		all, err := s.Store.Devices(ctx, store.StatusActive)
		if err != nil {
			return nil, err
		}
		counts, _ := s.Store.UpdateCounts(ctx)
		names := map[int64]string{}
		for _, d := range all {
			names[d.ID] = d.Name
		}
		type dev struct {
			Name     string `json:"name"`
			Kind     string `json:"kind"`
			Host     string `json:"host,omitempty"`
			State    string `json:"state"`
			Since    string `json:"since,omitempty"`
			Parent   string `json:"parent,omitempty"`
			Updates  int    `json:"updates,omitempty"`
			Security int    `json:"securityUpdates,omitempty"`
		}
		out := []dev{}
		for _, d := range all {
			if !p.SeesDevice(d) {
				continue
			}
			x := dev{Name: d.Name, Kind: d.Kind, Host: d.Host, State: d.State, Updates: counts[d.ID][0], Security: counts[d.ID][1]}
			if d.StateSince != nil {
				x.Since = time.Unix(*d.StateSince, 0).Format(time.RFC3339)
			}
			if d.ParentID != nil {
				x.Parent = names[*d.ParentID]
			}
			out = append(out, x)
		}
		return out, nil
	case "monitors":
		mons, err := s.Store.Monitors(ctx)
		if err != nil {
			return nil, err
		}
		type mon struct {
			Name      string   `json:"name"`
			Type      string   `json:"type"`
			Status    string   `json:"status"`
			Since     string   `json:"since,omitempty"`
			Message   string   `json:"message,omitempty"`
			Ms        *float64 `json:"responseMs,omitempty"`
			Uptime24h float64  `json:"uptime24h"`
			Uptime30d float64  `json:"uptime30d"`
			Cert      string   `json:"certificateExpires,omitempty"`
			Enabled   bool     `json:"enabled"`
		}
		now := time.Now()
		out := []mon{}
		for _, m := range mons {
			u1, _ := s.Store.MonitorUptime(ctx, m.ID, now.Add(-24*time.Hour).Unix())
			u30, _ := s.Store.MonitorUptime(ctx, m.ID, now.AddDate(0, 0, -30).Unix())
			x := mon{Name: m.Name, Type: m.Type, Status: m.Status, Message: m.LastMsg, Ms: m.LastMs, Uptime24h: u1, Uptime30d: u30, Enabled: m.Enabled}
			if m.StatusSince != nil {
				x.Since = time.Unix(*m.StatusSince, 0).Format(time.RFC3339)
			}
			if m.CertExpires != nil {
				x.Cert = time.Unix(*m.CertExpires, 0).Format("2006-01-02")
			}
			out = append(out, x)
		}
		return out, nil
	case "updates":
		comps, err := s.Store.PendingUpdates(ctx)
		if err != nil {
			return nil, err
		}
		type upd struct {
			Device    string `json:"device"`
			Component string `json:"component"`
			Group     string `json:"group"`
			Installed string `json:"installed"`
			Available string `json:"available"`
			Security  bool   `json:"security,omitempty"`
		}
		out := []upd{}
		cache := map[int64]store.Device{}
		for _, c := range comps {
			d, ok := cache[c.DeviceID]
			if !ok {
				d, _ = s.Store.Device(ctx, c.DeviceID)
				cache[c.DeviceID] = d
			}
			if !p.SeesDevice(d) {
				continue
			}
			out = append(out, upd{d.Name, strings.TrimSpace(c.Name), c.Group, c.Installed, c.Available, c.Security})
		}
		return out, nil
	}
	return nil, nil
}

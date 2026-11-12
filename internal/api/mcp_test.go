package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/KrX3D/Homewarden/internal/auth"
	"github.com/KrX3D/Homewarden/internal/store"
)

func TestMCP(t *testing.T) {
	h := newHarness(t)
	h.api.AttachOverview()
	h.api.AttachMCP()
	c := h.client()
	if code, _ := h.do(c, "POST", "/api/setup", map[string]string{"token": h.token, "username": "krx", "password": "a-long-password"}); code != 200 {
		t.Fatal("setup")
	}
	ctx := context.Background()
	st := h.api.Store
	u, _ := st.UserByName(ctx, "krx")
	st.CreateAPIToken(ctx, u.ID, "claude", auth.HashToken("hw_ro"), "hw_ro", true)
	st.CreateDevice(ctx, store.Device{Name: "Proxmox", Kind: "proxmox", Status: store.StatusActive})

	call := func(body string, origin string) (int, map[string]any) {
		req, _ := http.NewRequest("POST", h.srv.URL+"/api/mcp", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer hw_ro")
		req.Header.Set("Content-Type", "application/json")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	if code, _ := call(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`, ""); code != 404 {
		t.Fatalf("off by default: %d", code)
	}
	st.SetSetting(ctx, "mcp.enabled", "1")
	code, out := call(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, "")
	if code != 200 || out["result"].(map[string]any)["protocolVersion"] != "2025-06-18" {
		t.Fatalf("initialize (read-only token must work over POST): %d %v", code, out)
	}
	if code, _ := call(`{"jsonrpc":"2.0","method":"notifications/initialized"}`, ""); code != 202 {
		t.Fatalf("notification: %d", code)
	}
	_, out = call(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, "")
	if n := len(out["result"].(map[string]any)["tools"].([]any)); n != 4 {
		t.Fatalf("tools: %d", n)
	}
	_, out = call(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"devices","arguments":{}}}`, "")
	text := out["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, `"name":"Proxmox"`) {
		t.Fatalf("devices: %s", text)
	}
	_, out = call(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"overview"}}`, "")
	if out["result"].(map[string]any)["isError"] != nil {
		t.Fatalf("overview: %v", out)
	}
	if code, _ := call(`{"jsonrpc":"2.0","id":5,"method":"ping"}`, "http://evil.example"); code != 403 {
		t.Fatalf("foreign origin: %d", code)
	}
	if _, out := call(`{"jsonrpc":"2.0","id":6,"method":"nope"}`, ""); out["error"] == nil {
		t.Fatal("unknown method must fail")
	}
}

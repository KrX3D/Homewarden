package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/KrX3D/Homewarden/internal/auth"
)

func TestAPITokensReadOnlyAndSwitch(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	if code, _ := h.do(c, "POST", "/api/setup", map[string]string{"token": h.token, "username": "krx", "password": "a-long-password"}); code != 200 {
		t.Fatal("setup")
	}
	ctx := context.Background()
	u, _ := h.api.Store.UserByName(ctx, "krx")
	if _, err := h.api.Store.CreateAPIToken(ctx, u.ID, "full", auth.HashToken("hw_full"), "_full", false); err != nil {
		t.Fatal(err)
	}
	h.api.Store.CreateAPIToken(ctx, u.ID, "ro", auth.HashToken("hw_ro"), "hw_ro", true)

	call := func(method, path, tok string) (int, string) {
		req, _ := http.NewRequest(method, h.srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		e, _ := out["error"].(string)
		return resp.StatusCode, e
	}
	if code, _ := call("GET", "/api/me", "hw_ro"); code != 200 {
		t.Fatalf("read-only GET: %d", code)
	}
	if code, e := call("POST", "/api/me/totp/setup", "hw_ro"); code != 403 || e != "read_only_token" {
		t.Fatalf("read-only POST: %d %s", code, e)
	}
	if code, _ := call("POST", "/api/me/totp/setup", "hw_full"); code != 200 {
		t.Fatalf("full POST: %d", code)
	}
	h.api.Store.SetSetting(ctx, "api.enabled", "0")
	if code, e := call("GET", "/api/me", "hw_full"); code != 403 || e != "api_disabled" {
		t.Fatalf("disabled: %d %s", code, e)
	}
	if code, _ := h.do(c, "GET", "/api/me", nil); code != 200 {
		t.Fatalf("web session must keep working: %d", code)
	}
}

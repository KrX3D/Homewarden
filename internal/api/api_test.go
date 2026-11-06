package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/KrX3D/Homewarden/internal/auth"
	"github.com/KrX3D/Homewarden/internal/store"
	"github.com/KrX3D/Homewarden/internal/vault"
)

type harness struct {
	t     *testing.T
	srv   *httptest.Server
	api   *Server
	token string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	v, err := vault.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, v, BuildInfo{Version: "test"}, nil)
	tok, err := s.PrepareSetup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, api: s, token: tok, srv: httptest.NewServer(s)}
	t.Cleanup(h.srv.Close)
	return h
}

func (h *harness) client() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

func (h *harness) do(c *http.Client, method, path string, body any) (int, map[string]any) {
	h.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, h.srv.URL+path, &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(csrfHeader, "1")
	resp, err := c.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestSetupLoginFlow(t *testing.T) {
	h := newHarness(t)
	c := h.client()

	if code, out := h.do(c, "GET", "/api/meta", nil); code != 200 || out["needsSetup"] != true {
		t.Fatalf("meta: %d %v", code, out)
	}
	if code, out := h.do(c, "POST", "/api/setup", map[string]string{"token": "WRONG", "username": "krx", "password": "a-long-password"}); code != 403 || out["error"] != "invalid_setup_token" {
		t.Fatalf("wrong token: %d %v", code, out)
	}
	if code, out := h.do(c, "POST", "/api/setup", map[string]string{"token": h.token, "username": "krx", "password": "short"}); code != 400 || out["error"] != "weak_password" {
		t.Fatalf("weak: %d %v", code, out)
	}
	if code, out := h.do(c, "POST", "/api/setup", map[string]string{"token": h.token, "username": "krx", "password": "a-long-password"}); code != 200 {
		t.Fatalf("setup: %d %v", code, out)
	}
	if code, out := h.do(c, "GET", "/api/me", nil); code != 200 {
		t.Fatalf("me after setup: %d %v", code, out)
	}
	if code, _ := h.do(h.client(), "POST", "/api/setup", map[string]string{"token": h.token, "username": "x2", "password": "a-long-password"}); code != 409 {
		t.Fatalf("second setup must fail, got %d", code)
	}

	// Fresh client: login.
	c2 := h.client()
	if code, _ := h.do(c2, "GET", "/api/me", nil); code != 401 {
		t.Fatalf("anonymous me: %d", code)
	}
	if code, out := h.do(c2, "POST", "/api/auth/login", map[string]string{"username": "KRX", "password": "nope-nope-nope"}); code != 401 || out["error"] != "invalid_credentials" {
		t.Fatalf("bad login: %d %v", code, out)
	}
	if code, _ := h.do(c2, "POST", "/api/auth/login", map[string]string{"username": "KRX", "password": "a-long-password"}); code != 200 {
		t.Fatalf("login: %d", code)
	}

	// Enable TOTP, then login needs the code.
	_, setup := h.do(c2, "POST", "/api/me/totp/setup", nil)
	secret := setup["secret"].(string)
	code6, _ := auth.TOTPCode(secret, time.Now())
	if code, out := h.do(c2, "POST", "/api/me/totp/enable", map[string]string{"code": code6}); code != 200 {
		t.Fatalf("enable totp: %d %v", code, out)
	}
	c3 := h.client()
	if code, out := h.do(c3, "POST", "/api/auth/login", map[string]string{"username": "krx", "password": "a-long-password"}); code != 401 || out["error"] != "mfa_required" {
		t.Fatalf("mfa required: %d %v", code, out)
	}
	if code, _ := h.do(c3, "POST", "/api/auth/login", map[string]string{"username": "krx", "password": "a-long-password", "code": code6}); code != 200 {
		t.Fatalf("mfa login: %d", code)
	}
}

func TestCSRFHeaderRequired(t *testing.T) {
	h := newHarness(t)
	resp, err := http.Post(h.srv.URL+"/api/auth/login", "application/json", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("want 403 without csrf header, got %d", resp.StatusCode)
	}
}

func TestPermissionsAndLastAdmin(t *testing.T) {
	h := newHarness(t)
	admin := h.client()
	h.do(admin, "POST", "/api/setup", map[string]string{"token": h.token, "username": "admin", "password": "a-long-password"})

	viewer, _ := h.api.Store.RoleByName(context.Background(), "Viewer")
	if code, out := h.do(admin, "POST", "/api/users", map[string]any{"username": "guest", "password": "another-password", "roleId": viewer.ID}); code != 201 {
		t.Fatalf("create user: %d %v", code, out)
	}
	g := h.client()
	h.do(g, "POST", "/api/auth/login", map[string]string{"username": "guest", "password": "another-password"})
	if code, _ := h.do(g, "GET", "/api/users", nil); code != 403 {
		t.Fatalf("viewer must not list users, got %d", code)
	}

	_, me := h.do(admin, "GET", "/api/me", nil)
	myID := int64(me["user"].(map[string]any)["id"].(float64))
	if code, out := h.do(admin, "PUT", "/api/users/"+itoa(myID), map[string]any{"roleId": viewer.ID}); code != 409 || out["error"] != "last_admin" {
		t.Fatalf("demoting last admin: %d %v", code, out)
	}
}

func itoa(i int64) string { return json.Number(func() string { b, _ := json.Marshal(i); return string(b) }()).String() }

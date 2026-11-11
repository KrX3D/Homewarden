// Package api is Homewarden's HTTP API and the server for the embedded web interface.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/KrX3D/Homewarden/internal/agent"
	"github.com/KrX3D/Homewarden/internal/auth"
	"github.com/KrX3D/Homewarden/internal/automation"
	"github.com/KrX3D/Homewarden/internal/conn"
	"github.com/KrX3D/Homewarden/internal/monitor"
	"github.com/KrX3D/Homewarden/internal/notify"
	"github.com/KrX3D/Homewarden/internal/store"
	"github.com/KrX3D/Homewarden/internal/vault"
)

const (
	sessionCookie = "hw_session"
	sessionTTL    = 30 * 24 * time.Hour
	// csrfHeader must be present on every state-changing request. A browser only sends
	// a custom header cross-site after a CORS preflight, which this server never
	// approves, so a malicious page cannot forge one of these requests.
	csrfHeader = "X-Homewarden"
)

// BuildInfo identifies the running binary.
type BuildInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"buildDate"`
}

// Server holds the dependencies the handlers need.
type Server struct {
	Store  *store.Store
	Vault  *vault.Vault
	Build  BuildInfo
	Static http.Handler

	limiter  *auth.Limiter
	catalog  catalogCache
	mux      *http.ServeMux
	dev      DeviceDeps
	conn     *conn.Service
	sw       SoftwareDeps
	auto     *automation.Service
	agents   *agent.Hub
	notify   *notify.Service
	monitors *monitor.Runner
	baseCtx  context.Context
	mqtt     *automation.MQTT

	setupMu    sync.Mutex
	setupToken string

	oidc oidcState
}

// New builds the server and its routes.
func New(st *store.Store, v *vault.Vault, build BuildInfo, static http.Handler) *Server {
	s := &Server{Store: st, Vault: v, Build: build, Static: static, limiter: auth.NewLimiter(), mux: http.NewServeMux()}
	s.oidc.pending = map[string]oidcPending{}
	s.routes()
	return s
}

// SetBaseContext gives handlers a context that lives as long as the server, for work
// that must outlive a request (e.g. reconnecting MQTT).
func (s *Server) SetBaseContext(ctx context.Context) { s.baseCtx = ctx }

// Extra lets later packages register routes on the same mux.
func (s *Server) Handle(pattern string, h http.Handler) { s.mux.Handle(pattern, h) }

// PrepareSetup creates the one-time setup token when no user exists yet and returns
// it (empty when setup already happened).
func (s *Server) PrepareSetup(ctx context.Context) (string, error) {
	n, err := s.Store.UserCount(ctx)
	if err != nil || n > 0 {
		return "", err
	}
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	s.setupToken = auth.NewSetupToken()
	return s.setupToken, nil
}

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("GET /healthz", s.health)

	m.HandleFunc("GET /api/meta", s.meta)
	m.HandleFunc("POST /api/setup", s.setup)
	m.HandleFunc("POST /api/auth/login", s.login)
	m.HandleFunc("POST /api/auth/logout", s.logout)
	m.HandleFunc("GET /api/auth/oidc/start", s.oidcStart)
	m.HandleFunc("GET /api/auth/oidc/callback", s.oidcCallback)
	m.Handle("GET /api/settings/oidc", s.authed(auth.PermSettingsManage, s.getOIDC))
	m.Handle("PUT /api/settings/oidc", s.authed(auth.PermSettingsManage, s.putOIDC))

	m.Handle("GET /api/me", s.authed("", s.me))
	m.Handle("PUT /api/me/password", s.authed("", s.changePassword))
	m.Handle("PUT /api/me/prefs", s.authed("", s.setPrefs))
	m.Handle("POST /api/me/totp/setup", s.authed("", s.totpSetup))
	m.Handle("POST /api/me/totp/enable", s.authed("", s.totpEnable))
	m.Handle("POST /api/me/totp/disable", s.authed("", s.totpDisable))

	m.Handle("GET /api/users", s.authed(auth.PermUsersManage, s.listUsers))
	m.Handle("POST /api/users", s.authed(auth.PermUsersManage, s.createUser))
	m.Handle("PUT /api/users/{id}", s.authed(auth.PermUsersManage, s.updateUser))
	m.Handle("DELETE /api/users/{id}", s.authed(auth.PermUsersManage, s.deleteUser))
	m.Handle("POST /api/users/{id}/reset-totp", s.authed(auth.PermUsersManage, s.resetUserTOTP))

	m.Handle("GET /api/roles", s.authed(auth.PermUsersManage, s.listRoles))
	m.Handle("POST /api/roles", s.authed(auth.PermUsersManage, s.createRole))
	m.Handle("PUT /api/roles/{id}", s.authed(auth.PermUsersManage, s.updateRole))
	m.Handle("DELETE /api/roles/{id}", s.authed(auth.PermUsersManage, s.deleteRole))

	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { fail(w, http.StatusNotFound, "not_found") })
	if s.Static != nil {
		m.Handle("/", s.Static)
	}
}

// ServeHTTP applies the CSRF rule and dispatches.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Push heartbeats come from scripts and devices, authenticated by the token in the URL.
	if strings.HasPrefix(r.URL.Path, "/api/") && r.Method != http.MethodGet && r.Method != http.MethodHead && !strings.HasPrefix(r.URL.Path, "/api/push/") {
		// Bearer tokens are not sent automatically by browsers, so they cannot be forged
		// cross-site; only cookie sessions need the header.
		if r.Header.Get(csrfHeader) == "" && !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") && !strings.HasPrefix(r.URL.Path, "/api/agent/") {
			fail(w, http.StatusForbidden, "csrf")
			return
		}
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "same-origin")
	s.mux.ServeHTTP(w, r)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "ok", "version": s.Build.Version, "commit": s.Build.Commit, "buildDate": s.Build.BuildDate,
	})
}

func (s *Server) meta(w http.ResponseWriter, r *http.Request) {
	n, err := s.Store.UserCount(r.Context())
	if err != nil {
		internal(w, err)
		return
	}
	o := s.loadOIDC(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"sso":         map[string]any{"enabled": o.Enabled, "label": o.Label},
		"build":       s.Build,
		"needsSetup":  n == 0,
		"minPassword": auth.MinPasswordLength,
		"permissions": auth.Permissions,
	})
}

// ---- auth middleware

type ctxKey int

const (
	userKey ctxKey = iota
	permsKey
	sessionKey
)

// Principal is the logged-in user of a request.
type Principal struct {
	User        store.User
	Role        store.Role
	SessionHash string
}

// Can reports whether the principal has a permission.
func (p Principal) Can(perm string) bool { return auth.Has(p.Role.Permissions, perm) }

// SeesDevice reports whether the principal's role covers a device's group.
func (p Principal) SeesDevice(d store.Device) bool {
	if len(p.Role.Groups) == 0 || auth.Has(p.Role.Permissions, auth.PermAll) {
		return true
	}
	for _, g := range p.Role.Groups {
		if strings.EqualFold(g, d.GroupName) {
			return true
		}
	}
	return false
}

// PrincipalFrom returns the request's principal; only valid behind authed.
func PrincipalFrom(r *http.Request) Principal {
	p, _ := r.Context().Value(userKey).(Principal)
	return p
}

// Authed wraps a handler so it requires a session and, when perm is not empty, that
// permission. Exported for other packages registering routes.
func (s *Server) Authed(perm string, h http.HandlerFunc) http.Handler { return s.authed(perm, h) }

func (s *Server) authed(perm string, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			if !s.Store.APIEnabled(r.Context()) {
				FailMsg(w, http.StatusForbidden, "api_disabled", "API access is turned off in Settings, API")
				return
			}
			u, readOnly, err := s.Store.TokenUser(r.Context(), auth.HashToken(strings.TrimSpace(bearer)))
			if err != nil || u.Disabled {
				fail(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			if readOnly && r.Method != http.MethodGet && r.Method != http.MethodHead {
				FailMsg(w, http.StatusForbidden, "read_only_token", "this token may only read")
				return
			}
			role, err := s.Store.Role(r.Context(), u.RoleID)
			if err != nil {
				internal(w, err)
				return
			}
			p := Principal{User: u, Role: role}
			if perm != "" && !p.Can(perm) {
				fail(w, http.StatusForbidden, "forbidden")
				return
			}
			h(w, r.WithContext(context.WithValue(r.Context(), userKey, p)))
			return
		}
		c, err := r.Cookie(sessionCookie)
		if err != nil || c.Value == "" {
			fail(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		hash := auth.HashToken(c.Value)
		u, exp, err := s.Store.SessionUser(r.Context(), hash)
		if err != nil || u.Disabled {
			fail(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		role, err := s.Store.Role(r.Context(), u.RoleID)
		if err != nil {
			internal(w, err)
			return
		}
		p := Principal{User: u, Role: role, SessionHash: hash}
		if perm != "" && !p.Can(perm) {
			fail(w, http.StatusForbidden, "forbidden")
			return
		}
		// Sliding expiry: renew once less than half the lifetime is left.
		if time.Until(time.Unix(exp, 0)) < sessionTTL/2 {
			newExp := time.Now().Add(sessionTTL)
			if s.Store.ExtendSession(r.Context(), hash, newExp.Unix()) == nil {
				setSessionCookie(w, r, c.Value, newExp)
			}
		}
		h(w, r.WithContext(context.WithValue(r.Context(), userKey, p)))
	})
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string, exp time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", Expires: exp, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r),
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r)})
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---- JSON helpers

// APIError is the body of every error response. Code is stable and translated by the
// web interface; Message is extra detail for logs and power users.
type APIError struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
	Detail  any    `json:"detail,omitempty"`
}

// WriteJSON writes v as JSON.
func WriteJSON(w http.ResponseWriter, status int, v any) { writeJSON(w, status, v) }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Fail writes an error response with a stable code.
func Fail(w http.ResponseWriter, status int, code string) { fail(w, status, code) }

func fail(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, APIError{Error: code})
}

// FailMsg writes an error response with a message.
func FailMsg(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, APIError{Error: code, Message: msg})
}

// Internal logs err and answers 500.
func Internal(w http.ResponseWriter, err error) { internal(w, err) }

func internal(w http.ResponseWriter, err error) {
	log.Printf("internal error: %v", err)
	fail(w, http.StatusInternalServerError, "internal")
}

// ReadJSON decodes a request body into v, rejecting unknown or oversized input.
func ReadJSON(w http.ResponseWriter, r *http.Request, v any) bool { return readJSON(w, r, v) }

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		FailMsg(w, http.StatusBadRequest, "bad_request", err.Error())
		return false
	}
	return true
}

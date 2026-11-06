package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/KrX3D/Homewarden/internal/auth"
	"github.com/KrX3D/Homewarden/internal/store"
)

func validUsername(u string) bool {
	if len(u) < 2 || len(u) > 64 {
		return false
	}
	for _, c := range u {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._-@", c)) {
			return false
		}
	}
	return true
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u store.User) error {
	token, hash := auth.NewToken()
	exp := time.Now().Add(sessionTTL)
	if err := s.Store.CreateSession(r.Context(), hash, u.ID, exp.Unix()); err != nil {
		return err
	}
	_ = s.Store.TouchLogin(r.Context(), u.ID)
	setSessionCookie(w, r, token, exp)
	return nil
}

// setup creates the first admin. It needs the token from the container log so that
// nobody else on the network can claim a fresh instance first.
func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	var in struct{ Token, Username, Password string }
	if !readJSON(w, r, &in) {
		return
	}
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	n, err := s.Store.UserCount(r.Context())
	if err != nil {
		internal(w, err)
		return
	}
	if n > 0 || s.setupToken == "" {
		fail(w, http.StatusConflict, "setup_done")
		return
	}
	ip := "setup:" + clientIP(r)
	if d := s.limiter.Blocked(ip); d > 0 {
		writeJSON(w, http.StatusTooManyRequests, APIError{Error: "rate_limited", Detail: map[string]int{"retryAfter": int(math.Ceil(d.Seconds()))}})
		return
	}
	if !auth.EqualToken(strings.ToUpper(strings.TrimSpace(in.Token)), s.setupToken) {
		s.limiter.Fail(ip)
		fail(w, http.StatusForbidden, "invalid_setup_token")
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	if !validUsername(in.Username) {
		fail(w, http.StatusBadRequest, "invalid_username")
		return
	}
	hash, err := auth.HashPassword(in.Password)
	if errors.Is(err, auth.ErrWeakPassword) {
		fail(w, http.StatusBadRequest, "weak_password")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	admin, err := s.Store.RoleByName(r.Context(), "Admin")
	if err != nil {
		internal(w, err)
		return
	}
	u, err := s.Store.CreateUser(r.Context(), in.Username, hash, admin.ID)
	if err != nil {
		internal(w, err)
		return
	}
	s.setupToken = ""
	s.limiter.Success(ip)
	log.Printf("setup complete: admin account %q created", u.Username)
	if err := s.startSession(w, r, u); err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct{ Username, Password, Code string }
	if !readJSON(w, r, &in) {
		return
	}
	userKey := "u:" + strings.ToLower(strings.TrimSpace(in.Username))
	ipKey := "ip:" + clientIP(r)
	for _, k := range []string{userKey, ipKey} {
		if d := s.limiter.Blocked(k); d > 0 {
			writeJSON(w, http.StatusTooManyRequests, APIError{Error: "rate_limited", Detail: map[string]int{"retryAfter": int(math.Ceil(d.Seconds()))}})
			return
		}
	}
	failLogin := func() {
		s.limiter.Fail(userKey)
		s.limiter.Fail(ipKey)
		fail(w, http.StatusUnauthorized, "invalid_credentials")
	}
	u, err := s.Store.UserByName(r.Context(), strings.TrimSpace(in.Username))
	if errors.Is(err, store.ErrNotFound) {
		auth.BurnTime(in.Password)
		failLogin()
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	if !auth.CheckPassword(u.PasswordHash, in.Password) || u.Disabled {
		failLogin()
		return
	}
	if u.TOTPEnabled {
		if strings.TrimSpace(in.Code) == "" {
			// Password was right; ask for the second factor without counting a failure.
			fail(w, http.StatusUnauthorized, "mfa_required")
			return
		}
		secret, err := s.Vault.DecryptString(u.TOTPSecret)
		if err != nil {
			internal(w, err)
			return
		}
		if !auth.ValidTOTP(secret, in.Code, time.Now()) {
			s.limiter.Fail(userKey)
			s.limiter.Fail(ipKey)
			fail(w, http.StatusUnauthorized, "invalid_code")
			return
		}
	}
	s.limiter.Success(userKey)
	s.limiter.Success(ipKey)
	if err := s.startSession(w, r, u); err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.Store.DeleteSession(r.Context(), auth.HashToken(c.Value))
	}
	clearSessionCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	p := PrincipalFrom(r)
	writeJSON(w, http.StatusOK, map[string]any{"user": p.User, "role": p.Role})
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	p := PrincipalFrom(r)
	var in struct{ Current, New string }
	if !readJSON(w, r, &in) {
		return
	}
	if !auth.CheckPassword(p.User.PasswordHash, in.Current) {
		fail(w, http.StatusBadRequest, "wrong_password")
		return
	}
	hash, err := auth.HashPassword(in.New)
	if errors.Is(err, auth.ErrWeakPassword) {
		fail(w, http.StatusBadRequest, "weak_password")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	if err := s.Store.SetPassword(r.Context(), p.User.ID, hash); err != nil {
		internal(w, err)
		return
	}
	// Other devices logged in with the old password are signed out.
	_ = s.Store.DeleteUserSessions(r.Context(), p.User.ID, p.SessionHash)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) setPrefs(w http.ResponseWriter, r *http.Request) {
	var in map[string]any
	if !readJSON(w, r, &in) {
		return
	}
	b, _ := json.Marshal(in)
	if len(b) > 8192 {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	if err := s.Store.SetPrefs(r.Context(), PrincipalFrom(r).User.ID, b); err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, in)
}

// totpSetup creates a new pending secret. It only becomes active once the user proves
// their app has it by entering a code (totpEnable).
func (s *Server) totpSetup(w http.ResponseWriter, r *http.Request) {
	p := PrincipalFrom(r)
	if p.User.TOTPEnabled {
		fail(w, http.StatusConflict, "totp_already_enabled")
		return
	}
	secret := auth.NewTOTPSecret()
	enc, err := s.Vault.EncryptString(secret)
	if err != nil {
		internal(w, err)
		return
	}
	if err := s.Store.SetTOTP(r.Context(), p.User.ID, enc, false); err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"secret": secret, "url": auth.TOTPURL(secret, p.User.Username)})
}

func (s *Server) totpEnable(w http.ResponseWriter, r *http.Request) {
	p := PrincipalFrom(r)
	var in struct{ Code string }
	if !readJSON(w, r, &in) {
		return
	}
	if p.User.TOTPSecret == "" {
		fail(w, http.StatusBadRequest, "totp_not_started")
		return
	}
	secret, err := s.Vault.DecryptString(p.User.TOTPSecret)
	if err != nil {
		internal(w, err)
		return
	}
	if !auth.ValidTOTP(secret, in.Code, time.Now()) {
		fail(w, http.StatusBadRequest, "invalid_code")
		return
	}
	if err := s.Store.SetTOTP(r.Context(), p.User.ID, p.User.TOTPSecret, true); err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) totpDisable(w http.ResponseWriter, r *http.Request) {
	p := PrincipalFrom(r)
	var in struct{ Password string }
	if !readJSON(w, r, &in) {
		return
	}
	if !auth.CheckPassword(p.User.PasswordHash, in.Password) {
		fail(w, http.StatusBadRequest, "wrong_password")
		return
	}
	if err := s.Store.SetTOTP(r.Context(), p.User.ID, "", false); err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// PathID parses a numeric {id} path value.
func PathID(r *http.Request) (int64, error) {
	var id int64
	_, err := fmt.Sscan(r.PathValue("id"), &id)
	return id, err
}

package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/KrX3D/Homewarden/internal/auth"
	"github.com/KrX3D/Homewarden/internal/store"
)

// OIDCSettings configure single sign-on (Authentik, Authelia, Keycloak, ...).
type OIDCSettings struct {
	Enabled       bool   `json:"enabled"`
	Issuer        string `json:"issuer"`
	ClientID      string `json:"clientId"`
	ClientSecret  string `json:"clientSecret,omitempty"` // vault-encrypted when stored
	Label         string `json:"label"`
	UsernameClaim string `json:"usernameClaim"`
	GroupsClaim   string `json:"groupsClaim"`
	// RoleMap maps an IdP group to a role id; the first match wins.
	RoleMap []struct {
		Group  string `json:"group"`
		RoleID int64  `json:"roleId"`
	} `json:"roleMap"`
	// DefaultRoleID is used when no group matches; 0 refuses such users.
	DefaultRoleID int64 `json:"defaultRoleId"`
	AutoCreate    bool  `json:"autoCreate"`
}

type oidcPending struct {
	verifier string
	nonce    string
	redirect string
	expires  time.Time
}

type oidcState struct {
	mu      sync.Mutex
	pending map[string]oidcPending
}

func (s *Server) loadOIDC(ctx context.Context) OIDCSettings {
	raw, _ := s.Store.GetSetting(ctx, "oidc", "")
	var o OIDCSettings
	_ = json.Unmarshal([]byte(raw), &o)
	if o.UsernameClaim == "" {
		o.UsernameClaim = "preferred_username"
	}
	if o.GroupsClaim == "" {
		o.GroupsClaim = "groups"
	}
	if o.Label == "" {
		o.Label = "SSO"
	}
	return o
}

// redirectURL is where the IdP sends the browser back; it must be registered there.
func redirectURL(r *http.Request) string {
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	host := r.Host
	if fh := r.Header.Get("X-Forwarded-Host"); fh != "" {
		host = fh
	}
	return scheme + "://" + host + "/api/auth/oidc/callback"
}

func (s *Server) oidcConfig(ctx context.Context, r *http.Request) (*oidc.Provider, *oauth2.Config, OIDCSettings, error) {
	o := s.loadOIDC(ctx)
	if !o.Enabled || o.Issuer == "" || o.ClientID == "" {
		return nil, nil, o, errors.New("single sign-on is not set up")
	}
	secret := ""
	if o.ClientSecret != "" {
		plain, err := s.Vault.DecryptString(o.ClientSecret)
		if err != nil {
			return nil, nil, o, err
		}
		secret = plain
	}
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	p, err := oidc.NewProvider(pctx, strings.TrimRight(o.Issuer, "/"))
	if err != nil {
		return nil, nil, o, fmt.Errorf("discovery: %w", err)
	}
	cfg := &oauth2.Config{ClientID: o.ClientID, ClientSecret: secret, Endpoint: p.Endpoint(), RedirectURL: redirectURL(r),
		Scopes: []string{oidc.ScopeOpenID, "profile", "email", "groups"}}
	return p, cfg, o, nil
}

func randomString() string {
	b := make([]byte, 24)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *Server) oidcStart(w http.ResponseWriter, r *http.Request) {
	_, cfg, _, err := s.oidcConfig(r.Context(), r)
	if err != nil {
		http.Redirect(w, r, "/?sso_error="+urlQuery(err.Error()), http.StatusFound)
		return
	}
	// PKCE verifiers must be 43-128 characters (RFC 7636); the library makes a valid one.
	state, verifier, nonce := randomString(), oauth2.GenerateVerifier(), randomString()
	s.oidc.mu.Lock()
	for k, v := range s.oidc.pending {
		if time.Now().After(v.expires) {
			delete(s.oidc.pending, k)
		}
	}
	s.oidc.pending[state] = oidcPending{verifier: verifier, nonce: nonce, expires: time.Now().Add(10 * time.Minute)}
	s.oidc.mu.Unlock()
	url := cfg.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier))
	http.Redirect(w, r, url, http.StatusFound)
}

func urlQuery(s string) string {
	return strings.NewReplacer(" ", "+", "&", "%26", "#", "%23", "?", "%3F").Replace(s)
}

func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	fail := func(msg string) {
		log.Printf("sso: %s", msg)
		http.Redirect(w, r, "/?sso_error="+urlQuery(msg), http.StatusFound)
	}
	state := r.URL.Query().Get("state")
	s.oidc.mu.Lock()
	pend, ok := s.oidc.pending[state]
	delete(s.oidc.pending, state)
	s.oidc.mu.Unlock()
	if !ok || time.Now().After(pend.expires) {
		fail("the sign-in took too long or was started elsewhere; try again")
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		fail("the identity provider said: " + e)
		return
	}
	p, cfg, o, err := s.oidcConfig(r.Context(), r)
	if err != nil {
		fail(err.Error())
		return
	}
	tok, err := cfg.Exchange(r.Context(), r.URL.Query().Get("code"), oauth2.VerifierOption(pend.verifier))
	if err != nil {
		fail("code exchange failed: " + err.Error())
		return
	}
	rawID, _ := tok.Extra("id_token").(string)
	idt, err := p.Verifier(&oidc.Config{ClientID: o.ClientID}).Verify(r.Context(), rawID)
	if err != nil || idt.Nonce != pend.nonce {
		fail("the ID token could not be verified")
		return
	}
	var claims map[string]any
	_ = idt.Claims(&claims)
	// Authelia and others put groups only in userinfo; merge it in.
	if ui, err := p.UserInfo(r.Context(), oauth2.StaticTokenSource(tok)); err == nil {
		var extra map[string]any
		if ui.Claims(&extra) == nil {
			for k, v := range extra {
				if _, ok := claims[k]; !ok {
					claims[k] = v
				}
			}
		}
	}
	username, _ := claims[o.UsernameClaim].(string)
	if username == "" {
		username, _ = claims["email"].(string)
	}
	username = strings.TrimSpace(username)
	if !validUsername(username) {
		fail("the identity provider sent no usable user name (claim " + o.UsernameClaim + ")")
		return
	}
	groups := []string{}
	if g, ok := claims[o.GroupsClaim].([]any); ok {
		for _, x := range g {
			if s, ok := x.(string); ok {
				groups = append(groups, s)
			}
		}
	}
	roleID := o.DefaultRoleID
	for _, m := range o.RoleMap {
		for _, g := range groups {
			if strings.EqualFold(g, m.Group) {
				roleID = m.RoleID
				break
			}
		}
		if roleID != o.DefaultRoleID {
			break
		}
	}
	u, err := s.Store.UserByName(r.Context(), username)
	switch {
	case errors.Is(err, store.ErrNotFound):
		if !o.AutoCreate || roleID == 0 {
			fail("no Homewarden account for " + username)
			return
		}
		hash, _ := auth.HashPassword(randomString())
		if u, err = s.Store.CreateUser(r.Context(), username, hash, roleID); err != nil {
			fail("could not create the account")
			return
		}
		log.Printf("sso: created user %q", username)
	case err != nil:
		fail("database error")
		return
	default:
		if u.Disabled {
			fail("this account is disabled")
			return
		}
		// Group mapping is applied on every login so IdP changes take effect.
		if roleID != 0 && roleID != u.RoleID && len(o.RoleMap) > 0 {
			last, _ := s.wouldRemoveLastAdmin(r, u, roleID, false)
			if !last {
				u.RoleID = roleID
				_ = s.Store.UpdateUser(r.Context(), u)
			}
		}
	}
	if err := s.startSession(w, r, u); err != nil {
		fail("could not start the session")
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) getOIDC(w http.ResponseWriter, r *http.Request) {
	o := s.loadOIDC(r.Context())
	hasSecret := o.ClientSecret != ""
	o.ClientSecret = ""
	writeJSON(w, http.StatusOK, map[string]any{"settings": o, "hasSecret": hasSecret, "redirectUrl": redirectURL(r)})
}

func (s *Server) putOIDC(w http.ResponseWriter, r *http.Request) {
	var in OIDCSettings
	if !readJSON(w, r, &in) {
		return
	}
	old := s.loadOIDC(r.Context())
	if in.ClientSecret == "" {
		in.ClientSecret = old.ClientSecret
	} else {
		enc, err := s.Vault.EncryptString(in.ClientSecret)
		if err != nil {
			internal(w, err)
			return
		}
		in.ClientSecret = enc
	}
	in.Issuer = strings.TrimSpace(in.Issuer)
	if in.Enabled && (!strings.HasPrefix(in.Issuer, "https://") && !strings.HasPrefix(in.Issuer, "http://") || in.ClientID == "") {
		fail(w, http.StatusBadRequest, "oidc_incomplete")
		return
	}
	b, _ := json.Marshal(in)
	if err := s.Store.SetSetting(r.Context(), "oidc", string(b)); err != nil {
		internal(w, err)
		return
	}
	if in.Enabled {
		if _, _, _, err := s.oidcConfig(r.Context(), r); err != nil {
			FailMsg(w, http.StatusBadGateway, "oidc_discovery", err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

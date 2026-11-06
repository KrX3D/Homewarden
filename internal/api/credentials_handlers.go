package api

import (
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/KrX3D/Homewarden/internal/auth"
	"github.com/KrX3D/Homewarden/internal/conn"
	"github.com/KrX3D/Homewarden/internal/sshx"
	"github.com/KrX3D/Homewarden/internal/store"
)

// AttachCredentials registers the vault and device-connection routes.
func (s *Server) AttachCredentials(c *conn.Service) {
	s.conn = c
	m := s.mux
	m.Handle("GET /api/credentials", s.authed(auth.PermDevicesManage, s.listCredentials))
	m.Handle("POST /api/credentials", s.authed(auth.PermCredentialsManage, s.createCredential))
	m.Handle("PUT /api/credentials/{id}", s.authed(auth.PermCredentialsManage, s.updateCredential))
	m.Handle("DELETE /api/credentials/{id}", s.authed(auth.PermCredentialsManage, s.deleteCredential))

	m.Handle("PUT /api/devices/{id}/connection", s.authed(auth.PermDevicesManage, s.setConnection))
	m.Handle("POST /api/devices/{id}/connection/test", s.authed(auth.PermDevicesManage, s.testConnection))
	m.Handle("POST /api/devices/{id}/connection/install-key", s.authed(auth.PermDevicesManage, s.installKey))
	m.Handle("GET /api/devices/{id}/hostkey", s.authed(auth.PermDevicesView, s.getHostKey))
	m.Handle("DELETE /api/devices/{id}/hostkey", s.authed(auth.PermDevicesManage, s.forgetHostKey))
}

// listCredentials never returns secrets; device managers may list them to pick one.
func (s *Server) listCredentials(w http.ResponseWriter, r *http.Request) {
	cs, err := s.Store.Credentials(r.Context())
	if err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cs)
}

type credInput struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Username string `json:"username"`
	Secret   string `json:"secret"`   // password, token or pasted private key
	Generate bool   `json:"generate"` // ssh_key: create a new key pair
}

func (s *Server) sealCredential(c *store.Credential, in credInput, creating bool) string {
	switch c.Type {
	case store.CredSSHKey:
		var priv, pub string
		switch {
		case in.Generate:
			p, line, err := sshx.GenerateKey("homewarden")
			if err != nil {
				return "internal"
			}
			priv, pub = p, line
		case strings.TrimSpace(in.Secret) != "":
			line, err := sshx.PublicFromPrivate(strings.TrimSpace(in.Secret)+"\n", "homewarden")
			if err != nil {
				return "invalid_private_key"
			}
			priv, pub = strings.TrimSpace(in.Secret)+"\n", line
		case creating:
			return "secret_required"
		default:
			return ""
		}
		enc, err := s.Vault.EncryptString(priv)
		if err != nil {
			return "internal"
		}
		c.Secret, c.Public = enc, pub
	case store.CredPassword, store.CredToken:
		if in.Secret == "" {
			if creating {
				return "secret_required"
			}
			return ""
		}
		enc, err := s.Vault.EncryptString(in.Secret)
		if err != nil {
			return "internal"
		}
		c.Secret = enc
	default:
		return "invalid_type"
	}
	return ""
}

func (s *Server) createCredential(w http.ResponseWriter, r *http.Request) {
	var in credInput
	if !readJSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 128 {
		fail(w, http.StatusBadRequest, "invalid_name")
		return
	}
	c := store.Credential{Name: in.Name, Type: in.Type, Username: strings.TrimSpace(in.Username)}
	if code := s.sealCredential(&c, in, true); code != "" {
		fail(w, http.StatusBadRequest, code)
		return
	}
	out, err := s.Store.CreateCredential(r.Context(), c)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			fail(w, http.StatusConflict, "credential_name_taken")
			return
		}
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) loadCredential(w http.ResponseWriter, r *http.Request) (store.Credential, bool) {
	id, err := PathID(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request")
		return store.Credential{}, false
	}
	c, err := s.Store.Credential(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "not_found")
		return c, false
	}
	if err != nil {
		internal(w, err)
		return c, false
	}
	return c, true
}

func (s *Server) updateCredential(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadCredential(w, r)
	if !ok {
		return
	}
	var in credInput
	if !readJSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 128 {
		fail(w, http.StatusBadRequest, "invalid_name")
		return
	}
	c.Name, c.Username = in.Name, strings.TrimSpace(in.Username)
	if code := s.sealCredential(&c, in, false); code != "" {
		fail(w, http.StatusBadRequest, code)
		return
	}
	if err := s.Store.UpdateCredential(r.Context(), c); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			fail(w, http.StatusConflict, "credential_name_taken")
			return
		}
		internal(w, err)
		return
	}
	out, _ := s.Store.Credential(r.Context(), c.ID)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteCredential(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadCredential(w, r)
	if !ok {
		return
	}
	n, err := s.Store.CredentialUsage(r.Context(), c.ID)
	if err != nil {
		internal(w, err)
		return
	}
	if n > 0 {
		writeJSON(w, http.StatusConflict, APIError{Error: "credential_in_use", Detail: map[string]int{"n": n}})
		return
	}
	if err := s.Store.DeleteCredential(r.Context(), c.ID); err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- device connection

func (s *Server) setConnection(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadDevice(w, r)
	if !ok {
		return
	}
	var in conn.Settings
	if !readJSON(w, r, &in) {
		return
	}
	if in.Method != "ssh" && in.Method != "" {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	if in.Port < 0 || in.Port > 65535 {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	if in.CredentialID != 0 {
		if _, err := s.Store.Credential(r.Context(), in.CredentialID); err != nil {
			fail(w, http.StatusBadRequest, "invalid_credential")
			return
		}
	}
	cfg, err := conn.WithSettings(d, in)
	if err != nil {
		internal(w, err)
		return
	}
	d.Config = cfg
	if err := s.Store.UpdateDevice(r.Context(), d); err != nil {
		internal(w, err)
		return
	}
	s.dev.Events.Publish("device.changed", map[string]int64{"id": d.ID})
	out, _ := s.Store.Device(r.Context(), d.ID)
	writeJSON(w, http.StatusOK, out)
}

// sshError maps connection failures to stable codes the UI can explain.
func sshError(w http.ResponseWriter, err error) {
	var mismatch *sshx.HostKeyMismatch
	var opErr *net.OpError
	msg := err.Error()
	switch {
	case errors.Is(err, conn.ErrNotConfigured):
		fail(w, http.StatusBadRequest, "connection_not_configured")
	case errors.As(err, &mismatch):
		writeJSON(w, http.StatusConflict, APIError{Error: "host_key_changed", Message: msg, Detail: map[string]string{"expected": mismatch.Expected, "got": mismatch.Got}})
	case strings.Contains(msg, "unable to authenticate"):
		FailMsg(w, http.StatusBadGateway, "ssh_auth_failed", msg)
	case errors.As(err, &opErr) || strings.Contains(msg, "i/o timeout") || strings.Contains(msg, "connection refused") || strings.Contains(msg, "no route"):
		FailMsg(w, http.StatusBadGateway, "ssh_unreachable", msg)
	default:
		FailMsg(w, http.StatusBadGateway, "ssh_failed", msg)
	}
}

func (s *Server) testConnection(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadDevice(w, r)
	if !ok {
		return
	}
	f, err := s.conn.Test(r.Context(), d)
	if err != nil {
		sshError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (s *Server) installKey(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadDevice(w, r)
	if !ok {
		return
	}
	var in struct {
		User         string `json:"user"`
		Password     string `json:"password"`
		Port         int    `json:"port"`
		CredentialID int64  `json:"credentialId"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.User == "" || in.Password == "" || d.Host == "" {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	c, err := s.Store.Credential(r.Context(), in.CredentialID)
	if err != nil {
		fail(w, http.StatusBadRequest, "invalid_credential")
		return
	}
	if err := s.conn.InstallKey(r.Context(), d, in.Port, in.User, in.Password, c); err != nil {
		sshError(w, err)
		return
	}
	// Point the device at the key so the next steps just work.
	set := conn.FromDevice(d)
	set.Method, set.CredentialID, set.User = "ssh", c.ID, in.User
	if in.Port != 0 {
		set.Port = in.Port
	}
	cfg, err := conn.WithSettings(d, set)
	if err == nil {
		d.Config = cfg
		_ = s.Store.UpdateDevice(r.Context(), d)
	}
	s.dev.Events.Publish("device.changed", map[string]int64{"id": d.ID})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func hostAddr(d store.Device) string {
	return net.JoinHostPort(d.Host, strconv.Itoa(conn.FromDevice(d).Port))
}

func (s *Server) getHostKey(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadDevice(w, r)
	if !ok {
		return
	}
	k, err := s.Store.KnownHost(r.Context(), hostAddr(d))
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, k)
}

func (s *Server) forgetHostKey(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadDevice(w, r)
	if !ok {
		return
	}
	if err := s.Store.ForgetHost(r.Context(), hostAddr(d)); err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

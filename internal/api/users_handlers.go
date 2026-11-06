package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/KrX3D/Homewarden/internal/auth"
	"github.com/KrX3D/Homewarden/internal/store"
)

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.Store.Users(r.Context())
	if err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, users)
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username, Password string
		RoleID             int64
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	if !validUsername(in.Username) {
		fail(w, http.StatusBadRequest, "invalid_username")
		return
	}
	if _, err := s.Store.Role(r.Context(), in.RoleID); err != nil {
		fail(w, http.StatusBadRequest, "invalid_role")
		return
	}
	if _, err := s.Store.UserByName(r.Context(), in.Username); err == nil {
		fail(w, http.StatusConflict, "username_taken")
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
	u, err := s.Store.CreateUser(r.Context(), in.Username, hash, in.RoleID)
	if err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

// wouldRemoveLastAdmin reports whether changing user u to newRole/disabled leaves no
// enabled admin.
func (s *Server) wouldRemoveLastAdmin(r *http.Request, u store.User, newRoleID int64, disabled bool) (bool, error) {
	role, err := s.Store.Role(r.Context(), u.RoleID)
	if err != nil {
		return false, err
	}
	isAdmin := role.Builtin && role.Name == "Admin" && !u.Disabled
	if !isAdmin {
		return false, nil
	}
	stillAdmin := newRoleID == u.RoleID && !disabled
	if stillAdmin {
		return false, nil
	}
	n, err := s.Store.AdminCount(r.Context())
	return n <= 1, err
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	u, err := s.Store.User(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	var in struct {
		Username *string
		RoleID   *int64
		Disabled *bool
		Password *string
	}
	if !readJSON(w, r, &in) {
		return
	}
	next := u
	if in.Username != nil {
		next.Username = strings.TrimSpace(*in.Username)
		if !validUsername(next.Username) {
			fail(w, http.StatusBadRequest, "invalid_username")
			return
		}
		if other, err := s.Store.UserByName(r.Context(), next.Username); err == nil && other.ID != u.ID {
			fail(w, http.StatusConflict, "username_taken")
			return
		}
	}
	if in.RoleID != nil {
		if _, err := s.Store.Role(r.Context(), *in.RoleID); err != nil {
			fail(w, http.StatusBadRequest, "invalid_role")
			return
		}
		next.RoleID = *in.RoleID
	}
	if in.Disabled != nil {
		next.Disabled = *in.Disabled
	}
	last, err := s.wouldRemoveLastAdmin(r, u, next.RoleID, next.Disabled)
	if err != nil {
		internal(w, err)
		return
	}
	if last {
		fail(w, http.StatusConflict, "last_admin")
		return
	}
	if in.Password != nil {
		hash, err := auth.HashPassword(*in.Password)
		if errors.Is(err, auth.ErrWeakPassword) {
			fail(w, http.StatusBadRequest, "weak_password")
			return
		}
		if err != nil {
			internal(w, err)
			return
		}
		if err := s.Store.SetPassword(r.Context(), u.ID, hash); err != nil {
			internal(w, err)
			return
		}
		_ = s.Store.DeleteUserSessions(r.Context(), u.ID, "")
	}
	if err := s.Store.UpdateUser(r.Context(), next); err != nil {
		internal(w, err)
		return
	}
	if next.Disabled {
		_ = s.Store.DeleteUserSessions(r.Context(), u.ID, "")
	}
	out, _ := s.Store.User(r.Context(), u.ID)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	if id == PrincipalFrom(r).User.ID {
		fail(w, http.StatusConflict, "cannot_delete_self")
		return
	}
	u, err := s.Store.User(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	last, err := s.wouldRemoveLastAdmin(r, u, 0, true)
	if err != nil {
		internal(w, err)
		return
	}
	if last {
		fail(w, http.StatusConflict, "last_admin")
		return
	}
	if err := s.Store.DeleteUser(r.Context(), id); err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) resetUserTOTP(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	if err := s.Store.SetTOTP(r.Context(), id, "", false); err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- roles

func (s *Server) listRoles(w http.ResponseWriter, r *http.Request) {
	roles, err := s.Store.Roles(r.Context())
	if err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, roles)
}

func cleanPerms(in []string) ([]string, bool) {
	out := []string{}
	seen := map[string]bool{}
	for _, p := range in {
		if !auth.ValidPermission(p) || p == auth.PermAll {
			return nil, false
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out, true
}

func (s *Server) createRole(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name        string
		Permissions []string
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	perms, ok := cleanPerms(in.Permissions)
	if in.Name == "" || len(in.Name) > 64 || !ok {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	if _, err := s.Store.RoleByName(r.Context(), in.Name); err == nil {
		fail(w, http.StatusConflict, "role_name_taken")
		return
	}
	role, err := s.Store.CreateRole(r.Context(), in.Name, perms)
	if err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, role)
}

func (s *Server) updateRole(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	role, err := s.Store.Role(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "not_found")
		return
	}
	if role.Builtin {
		fail(w, http.StatusConflict, "role_builtin")
		return
	}
	var in struct {
		Name        string
		Permissions []string
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	perms, ok := cleanPerms(in.Permissions)
	if in.Name == "" || len(in.Name) > 64 || !ok {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	if other, err := s.Store.RoleByName(r.Context(), in.Name); err == nil && other.ID != id {
		fail(w, http.StatusConflict, "role_name_taken")
		return
	}
	if err := s.Store.UpdateRole(r.Context(), id, in.Name, perms); err != nil {
		internal(w, err)
		return
	}
	out, _ := s.Store.Role(r.Context(), id)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteRole(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	role, err := s.Store.Role(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "not_found")
		return
	}
	if role.Builtin {
		fail(w, http.StatusConflict, "role_builtin")
		return
	}
	used, err := s.Store.RoleInUse(r.Context(), id)
	if err != nil {
		internal(w, err)
		return
	}
	if used {
		fail(w, http.StatusConflict, "role_in_use")
		return
	}
	if err := s.Store.DeleteRole(r.Context(), id); err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

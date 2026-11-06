package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// Role is a named set of permissions.
type Role struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Builtin     bool     `json:"builtin"`
	Permissions []string `json:"permissions"`
	// Groups limits the role to devices in these groups; empty = all devices.
	Groups []string `json:"groups"`
}

// User is an account. Secrets are never serialised.
type User struct {
	ID           int64           `json:"id"`
	Username     string          `json:"username"`
	PasswordHash string          `json:"-"`
	RoleID       int64           `json:"roleId"`
	TOTPSecret   string          `json:"-"`
	TOTPEnabled  bool            `json:"totpEnabled"`
	Disabled     bool            `json:"disabled"`
	Prefs        json.RawMessage `json:"prefs"`
	CreatedAt    int64           `json:"createdAt"`
	LastLogin    *int64          `json:"lastLogin"`
}

// ---- roles

func scanRole(row interface{ Scan(...any) error }) (Role, error) {
	var r Role
	var perms, groups string
	if err := row.Scan(&r.ID, &r.Name, &r.Builtin, &perms, &groups); err != nil {
		return r, err
	}
	if err := json.Unmarshal([]byte(perms), &r.Permissions); err != nil {
		return r, err
	}
	_ = json.Unmarshal([]byte(groups), &r.Groups)
	if r.Groups == nil {
		r.Groups = []string{}
	}
	return r, nil
}

// Roles lists all roles.
func (s *Store) Roles(ctx context.Context) ([]Role, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT id, name, builtin, permissions, device_groups FROM roles ORDER BY builtin DESC, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Role{}
	for rows.Next() {
		r, err := scanRole(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Role returns one role.
func (s *Store) Role(ctx context.Context, id int64) (Role, error) {
	r, err := scanRole(s.DB.QueryRowContext(ctx, "SELECT id, name, builtin, permissions, device_groups FROM roles WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// RoleByName returns a role by name.
func (s *Store) RoleByName(ctx context.Context, name string) (Role, error) {
	r, err := scanRole(s.DB.QueryRowContext(ctx, "SELECT id, name, builtin, permissions, device_groups FROM roles WHERE name = ?", name))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// CreateRole adds a custom role.
func (s *Store) CreateRole(ctx context.Context, name string, perms, groups []string) (Role, error) {
	b, _ := json.Marshal(perms)
	if groups == nil {
		groups = []string{}
	}
	g, _ := json.Marshal(groups)
	res, err := s.DB.ExecContext(ctx, "INSERT INTO roles(name, builtin, permissions, device_groups) VALUES(?, 0, ?, ?)", name, string(b), string(g))
	if err != nil {
		return Role{}, err
	}
	id, _ := res.LastInsertId()
	return s.Role(ctx, id)
}

// UpdateRole renames a custom role and replaces its permissions.
func (s *Store) UpdateRole(ctx context.Context, id int64, name string, perms, groups []string) error {
	b, _ := json.Marshal(perms)
	if groups == nil {
		groups = []string{}
	}
	g, _ := json.Marshal(groups)
	_, err := s.DB.ExecContext(ctx, "UPDATE roles SET name = ?, permissions = ?, device_groups = ? WHERE id = ? AND builtin = 0", name, string(b), string(g), id)
	return err
}

// DeleteRole removes a custom role that no user has.
func (s *Store) DeleteRole(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM roles WHERE id = ? AND builtin = 0", id)
	return err
}

// RoleInUse reports whether any user has the role.
func (s *Store) RoleInUse(ctx context.Context, id int64) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE role_id = ?", id).Scan(&n)
	return n > 0, err
}

// ---- users

const userCols = "id, username, password_hash, role_id, totp_secret, totp_enabled, disabled, prefs, created_at, last_login"

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	var prefs string
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.RoleID, &u.TOTPSecret, &u.TOTPEnabled, &u.Disabled, &prefs, &u.CreatedAt, &u.LastLogin)
	u.Prefs = json.RawMessage(prefs)
	return u, err
}

// UserCount returns how many users exist; zero means setup has not run.
func (s *Store) UserCount(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&n)
	return n, err
}

// Users lists all users.
func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT "+userCols+" FROM users ORDER BY username")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// User returns one user.
func (s *Store) User(ctx context.Context, id int64) (User, error) {
	u, err := scanUser(s.DB.QueryRowContext(ctx, "SELECT "+userCols+" FROM users WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// UserByName returns a user by username (case-insensitive).
func (s *Store) UserByName(ctx context.Context, name string) (User, error) {
	u, err := scanUser(s.DB.QueryRowContext(ctx, "SELECT "+userCols+" FROM users WHERE username = ?", name))
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// CreateUser adds a user.
func (s *Store) CreateUser(ctx context.Context, username, hash string, roleID int64) (User, error) {
	res, err := s.DB.ExecContext(ctx,
		"INSERT INTO users(username, password_hash, role_id, created_at) VALUES(?, ?, ?, ?)", username, hash, roleID, now())
	if err != nil {
		return User{}, err
	}
	id, _ := res.LastInsertId()
	return s.User(ctx, id)
}

// UpdateUser stores role, disabled flag and username changes.
func (s *Store) UpdateUser(ctx context.Context, u User) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE users SET username = ?, role_id = ?, disabled = ? WHERE id = ?", u.Username, u.RoleID, u.Disabled, u.ID)
	return err
}

// SetPassword replaces a user's password hash.
func (s *Store) SetPassword(ctx context.Context, id int64, hash string) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE users SET password_hash = ? WHERE id = ?", hash, id)
	return err
}

// SetTOTP stores the (encrypted) TOTP secret and whether it is active.
func (s *Store) SetTOTP(ctx context.Context, id int64, encSecret string, enabled bool) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE users SET totp_secret = ?, totp_enabled = ? WHERE id = ?", encSecret, enabled, id)
	return err
}

// SetPrefs stores the user's UI preferences (theme, language, device layout).
func (s *Store) SetPrefs(ctx context.Context, id int64, prefs json.RawMessage) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE users SET prefs = ? WHERE id = ?", string(prefs), id)
	return err
}

// TouchLogin records a successful login.
func (s *Store) TouchLogin(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE users SET last_login = ? WHERE id = ?", now(), id)
	return err
}

// DeleteUser removes a user and their sessions.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM users WHERE id = ?", id)
	return err
}

// AdminCount counts enabled users holding the Admin role, so the last one cannot be
// removed or demoted.
func (s *Store) AdminCount(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM users u JOIN roles r ON r.id = u.role_id WHERE r.name = 'Admin' AND r.builtin = 1 AND u.disabled = 0").Scan(&n)
	return n, err
}

// ---- sessions

// CreateSession stores a session by the hash of its token.
func (s *Store) CreateSession(ctx context.Context, tokenHash string, userID, expires int64) error {
	_, err := s.DB.ExecContext(ctx, "INSERT INTO sessions(token_hash, user_id, created_at, expires_at) VALUES(?, ?, ?, ?)",
		tokenHash, userID, now(), expires)
	return err
}

// SessionUser returns the user of a live session.
func (s *Store) SessionUser(ctx context.Context, tokenHash string) (User, int64, error) {
	var uid, exp int64
	err := s.DB.QueryRowContext(ctx, "SELECT user_id, expires_at FROM sessions WHERE token_hash = ?", tokenHash).Scan(&uid, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, 0, ErrNotFound
	}
	if err != nil {
		return User{}, 0, err
	}
	if exp < now() {
		s.DeleteSession(ctx, tokenHash)
		return User{}, 0, ErrNotFound
	}
	u, err := s.User(ctx, uid)
	return u, exp, err
}

// ExtendSession moves a session's expiry.
func (s *Store) ExtendSession(ctx context.Context, tokenHash string, expires int64) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE sessions SET expires_at = ? WHERE token_hash = ?", expires, tokenHash)
	return err
}

// DeleteSession logs a session out.
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", tokenHash)
	return err
}

// DeleteUserSessions logs a user out everywhere, except the session given.
func (s *Store) DeleteUserSessions(ctx context.Context, userID int64, keepHash string) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ? AND token_hash <> ?", userID, keepHash)
	return err
}

// PurgeSessions removes expired sessions.
func (s *Store) PurgeSessions(ctx context.Context) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at < ?", now())
	return err
}

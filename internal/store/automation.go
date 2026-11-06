package store

import (
	"context"
	"database/sql"
	"errors"
)

// APIToken is a personal token for scripts, Home Assistant and the like. It acts with
// its user's role.
type APIToken struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"userId"`
	Name      string `json:"name"`
	CreatedAt int64  `json:"createdAt"`
	LastUsed  *int64 `json:"lastUsed"`
}

// CreateAPIToken stores a token hash.
func (s *Store) CreateAPIToken(ctx context.Context, userID int64, name, hash string) (APIToken, error) {
	res, err := s.DB.ExecContext(ctx, "INSERT INTO api_tokens(user_id, name, token_hash, created_at) VALUES(?, ?, ?, ?)", userID, name, hash, now())
	if err != nil {
		return APIToken{}, err
	}
	id, _ := res.LastInsertId()
	return APIToken{ID: id, UserID: userID, Name: name, CreatedAt: now()}, nil
}

// APITokens lists a user's tokens.
func (s *Store) APITokens(ctx context.Context, userID int64) ([]APIToken, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT id, user_id, name, created_at, last_used FROM api_tokens WHERE user_id = ? ORDER BY id", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIToken{}
	for rows.Next() {
		var t APIToken
		if err := rows.Scan(&t.ID, &t.UserID, &t.Name, &t.CreatedAt, &t.LastUsed); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteAPIToken removes one of a user's tokens.
func (s *Store) DeleteAPIToken(ctx context.Context, userID, id int64) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM api_tokens WHERE id = ? AND user_id = ?", id, userID)
	return err
}

// TokenUser resolves a token hash to its user and records the use.
func (s *Store) TokenUser(ctx context.Context, hash string) (User, error) {
	var uid int64
	err := s.DB.QueryRowContext(ctx, "SELECT user_id FROM api_tokens WHERE token_hash = ?", hash).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	_, _ = s.DB.ExecContext(ctx, "UPDATE api_tokens SET last_used = ? WHERE token_hash = ?", now(), hash)
	return s.User(ctx, uid)
}

// PendingAction is something scheduled while the device was offline.
type PendingAction struct {
	DeviceID int64  `json:"deviceId"`
	Action   string `json:"action"`
	Params   string `json:"params"`
	Since    int64  `json:"since"`
}

// AddPending remembers an action to run when the device is next ready.
func (s *Store) AddPending(ctx context.Context, p PendingAction) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO pending_actions(device_id, action, params, since) VALUES(?, ?, ?, ?)
		ON CONFLICT(device_id, action) DO UPDATE SET params = excluded.params`, p.DeviceID, p.Action, p.Params, now())
	return err
}

// TakePending returns and removes a device's pending actions.
func (s *Store) TakePending(ctx context.Context, deviceID int64) ([]PendingAction, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT device_id, action, params, since FROM pending_actions WHERE device_id = ?", deviceID)
	if err != nil {
		return nil, err
	}
	out := []PendingAction{}
	for rows.Next() {
		var p PendingAction
		if err := rows.Scan(&p.DeviceID, &p.Action, &p.Params, &p.Since); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, p)
	}
	rows.Close()
	_, err = s.DB.ExecContext(ctx, "DELETE FROM pending_actions WHERE device_id = ?", deviceID)
	return out, err
}

// Pending lists a device's pending actions without removing them.
func (s *Store) Pending(ctx context.Context, deviceID int64) ([]PendingAction, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT device_id, action, params, since FROM pending_actions WHERE device_id = ?", deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PendingAction{}
	for rows.Next() {
		var p PendingAction
		if err := rows.Scan(&p.DeviceID, &p.Action, &p.Params, &p.Since); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

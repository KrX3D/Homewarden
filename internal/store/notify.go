package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// NotifyChannel is a destination for notifications. Config is vault-encrypted because
// webhook URLs and bot tokens are secrets.
type NotifyChannel struct {
	ID        int64    `json:"id"`
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	Config    string   `json:"-"`
	Events    []string `json:"events"`
	Enabled   bool     `json:"enabled"`
	CreatedAt int64    `json:"createdAt"`
}

func scanChannel(row interface{ Scan(...any) error }) (NotifyChannel, error) {
	var c NotifyChannel
	var ev string
	err := row.Scan(&c.ID, &c.Name, &c.Type, &c.Config, &ev, &c.Enabled, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	_ = json.Unmarshal([]byte(ev), &c.Events)
	if c.Events == nil {
		c.Events = []string{}
	}
	return c, err
}

const chanCols = "id, name, type, config, events, enabled, created_at"

// NotifyChannels lists channels.
func (s *Store) NotifyChannels(ctx context.Context) ([]NotifyChannel, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT "+chanCols+" FROM notify_channels ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotifyChannel{}
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// NotifyChannel returns one channel.
func (s *Store) NotifyChannel(ctx context.Context, id int64) (NotifyChannel, error) {
	return scanChannel(s.DB.QueryRowContext(ctx, "SELECT "+chanCols+" FROM notify_channels WHERE id = ?", id))
}

// SaveNotifyChannel inserts (ID 0) or updates a channel.
func (s *Store) SaveNotifyChannel(ctx context.Context, c NotifyChannel) (NotifyChannel, error) {
	ev, _ := json.Marshal(c.Events)
	if c.ID == 0 {
		res, err := s.DB.ExecContext(ctx, "INSERT INTO notify_channels(name, type, config, events, enabled, created_at) VALUES(?, ?, ?, ?, ?, ?)",
			c.Name, c.Type, c.Config, string(ev), c.Enabled, now())
		if err != nil {
			return c, err
		}
		c.ID, _ = res.LastInsertId()
	} else if _, err := s.DB.ExecContext(ctx, "UPDATE notify_channels SET name = ?, type = ?, config = ?, events = ?, enabled = ? WHERE id = ?",
		c.Name, c.Type, c.Config, string(ev), c.Enabled, c.ID); err != nil {
		return c, err
	}
	return s.NotifyChannel(ctx, c.ID)
}

// DeleteNotifyChannel removes a channel.
func (s *Store) DeleteNotifyChannel(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM notify_channels WHERE id = ?", id)
	return err
}

// MarkSent records that a notification key was sent; it returns false when it already
// was, so the same update or warning is not reported twice.
func (s *Store) MarkSent(ctx context.Context, key string) (bool, error) {
	res, err := s.DB.ExecContext(ctx, "INSERT OR IGNORE INTO notify_sent(key, at) VALUES(?, ?)", key, now())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ClearSent forgets keys with a prefix (e.g. a warning that went away may fire again).
func (s *Store) ClearSent(ctx context.Context, prefix string) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM notify_sent WHERE key LIKE ? ESCAPE '\\'", escapeLike(prefix)+"%")
	return err
}

func escapeLike(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '%' || r == '_' || r == '\\' {
			out = append(out, '\\')
		}
		out = append(out, r)
	}
	return string(out)
}

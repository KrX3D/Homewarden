package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// Monitor is an uptime check that is not tied to device presence: a web page, a port,
// a DNS record, or a push heartbeat.
type Monitor struct {
	ID          int64           `json:"id"`
	Name        string          `json:"name"`
	Type        string          `json:"type"` // http | tcp | ping | dns | push
	Config      json.RawMessage `json:"config"`
	DeviceID    *int64          `json:"deviceId"`
	Interval    int             `json:"interval"`
	Enabled     bool            `json:"enabled"`
	Public      bool            `json:"public"`
	PushToken   string          `json:"pushToken,omitempty"`
	Status      string          `json:"status"` // up | down | unknown | paused
	StatusSince *int64          `json:"statusSince"`
	LastCheck   *int64          `json:"lastCheck"`
	LastMs      *float64        `json:"lastMs"`
	LastMsg     string          `json:"lastMsg"`
	CertExpires *int64          `json:"certExpires"`
	CreatedAt   int64           `json:"createdAt"`
}

const monCols = "id, name, type, config, device_id, interval_s, enabled, public, push_token, status, status_since, last_check, last_ms, last_msg, cert_expires, created_at"

func scanMonitor(row interface{ Scan(...any) error }) (Monitor, error) {
	var m Monitor
	var cfg string
	err := row.Scan(&m.ID, &m.Name, &m.Type, &cfg, &m.DeviceID, &m.Interval, &m.Enabled, &m.Public, &m.PushToken, &m.Status,
		&m.StatusSince, &m.LastCheck, &m.LastMs, &m.LastMsg, &m.CertExpires, &m.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	m.Config = json.RawMessage(cfg)
	return m, err
}

// Monitors lists all monitors.
func (s *Store) Monitors(ctx context.Context) ([]Monitor, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT "+monCols+" FROM monitors ORDER BY name COLLATE NOCASE")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Monitor{}
	for rows.Next() {
		m, err := scanMonitor(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Monitor returns one monitor.
func (s *Store) Monitor(ctx context.Context, id int64) (Monitor, error) {
	return scanMonitor(s.DB.QueryRowContext(ctx, "SELECT "+monCols+" FROM monitors WHERE id = ?", id))
}

// MonitorByPushToken finds a push monitor.
func (s *Store) MonitorByPushToken(ctx context.Context, token string) (Monitor, error) {
	return scanMonitor(s.DB.QueryRowContext(ctx, "SELECT "+monCols+" FROM monitors WHERE push_token = ? AND push_token <> ''", token))
}

// SaveMonitor inserts (ID 0) or updates the editable fields.
func (s *Store) SaveMonitor(ctx context.Context, m Monitor) (Monitor, error) {
	if len(m.Config) == 0 {
		m.Config = json.RawMessage("{}")
	}
	if m.ID == 0 {
		res, err := s.DB.ExecContext(ctx, `INSERT INTO monitors(name, type, config, device_id, interval_s, enabled, public, push_token, created_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`, m.Name, m.Type, string(m.Config), m.DeviceID, m.Interval, m.Enabled, m.Public, m.PushToken, now())
		if err != nil {
			return m, err
		}
		m.ID, _ = res.LastInsertId()
	} else if _, err := s.DB.ExecContext(ctx, `UPDATE monitors SET name = ?, type = ?, config = ?, device_id = ?, interval_s = ?, enabled = ?, public = ? WHERE id = ?`,
		m.Name, m.Type, string(m.Config), m.DeviceID, m.Interval, m.Enabled, m.Public, m.ID); err != nil {
		return m, err
	}
	return s.Monitor(ctx, m.ID)
}

// DeleteMonitor removes a monitor and its history.
func (s *Store) DeleteMonitor(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM monitors WHERE id = ?", id)
	return err
}

// RecordMonitor stores one result and the monitor's current status.
func (s *Store) RecordMonitor(ctx context.Context, id int64, ok bool, ms *float64, msg, status string, changed bool, cert *int64) error {
	t := now()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "INSERT INTO monitor_results(monitor_id, at, ok, ms) VALUES(?, ?, ?, ?)", id, t, ok, ms); err != nil {
		return err
	}
	q := "UPDATE monitors SET last_check = ?, last_ms = ?, last_msg = ?, status = ?, cert_expires = COALESCE(?, cert_expires)"
	args := []any{t, ms, msg, status, cert}
	if changed {
		q += ", status_since = ?"
		args = append(args, t)
	}
	q += " WHERE id = ?"
	args = append(args, id)
	if _, err := tx.ExecContext(ctx, q, args...); err != nil {
		return err
	}
	return tx.Commit()
}

// MonitorPoint is one result.
type MonitorPoint struct {
	At int64    `json:"at"`
	OK bool     `json:"ok"`
	Ms *float64 `json:"ms"`
}

// MonitorResults returns results since a time, oldest first.
func (s *Store) MonitorResults(ctx context.Context, id, since int64) ([]MonitorPoint, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT at, ok, ms FROM monitor_results WHERE monitor_id = ? AND at >= ? ORDER BY at", id, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MonitorPoint{}
	for rows.Next() {
		var p MonitorPoint
		if err := rows.Scan(&p.At, &p.OK, &p.Ms); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// MonitorUptime returns the share of successful checks since a time (-1 when none).
func (s *Store) MonitorUptime(ctx context.Context, id, since int64) (float64, error) {
	var n, ok sql.NullInt64
	err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*), SUM(ok) FROM monitor_results WHERE monitor_id = ? AND at >= ?", id, since).Scan(&n, &ok)
	if err != nil || !n.Valid || n.Int64 == 0 {
		return -1, err
	}
	return float64(ok.Int64) / float64(n.Int64), nil
}

// PruneMonitorResults removes results older than a time.
func (s *Store) PruneMonitorResults(ctx context.Context, before int64) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM monitor_results WHERE at < ?", before)
	return err
}

// Maintenance is a window during which alerts are muted (all devices when DeviceID is nil).
type Maintenance struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	DeviceID *int64 `json:"deviceId"`
	Starts   int64  `json:"starts"`
	Ends     int64  `json:"ends"`
}

// MaintenanceWindows lists windows that have not ended.
func (s *Store) MaintenanceWindows(ctx context.Context) ([]Maintenance, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT id, title, device_id, starts, ends FROM maintenance WHERE ends > ? ORDER BY starts", now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Maintenance{}
	for rows.Next() {
		var m Maintenance
		if err := rows.Scan(&m.ID, &m.Title, &m.DeviceID, &m.Starts, &m.Ends); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AddMaintenance stores a window.
func (s *Store) AddMaintenance(ctx context.Context, m Maintenance) error {
	_, err := s.DB.ExecContext(ctx, "INSERT INTO maintenance(title, device_id, starts, ends) VALUES(?, ?, ?, ?)", m.Title, m.DeviceID, m.Starts, m.Ends)
	return err
}

// DeleteMaintenance removes a window.
func (s *Store) DeleteMaintenance(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM maintenance WHERE id = ?", id)
	return err
}

// InMaintenance reports whether a device (or everything, deviceID 0) is in a window now.
func (s *Store) InMaintenance(ctx context.Context, deviceID int64) bool {
	var n int
	t := now()
	_ = s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM maintenance WHERE starts <= ? AND ends > ? AND (device_id IS NULL OR device_id = ?)", t, t, deviceID).Scan(&n)
	return n > 0
}

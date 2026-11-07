package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

// Device statuses.
const (
	StatusActive     = "active"     // added and monitored
	StatusDiscovered = "discovered" // found, waiting in the inbox
	StatusIgnored    = "ignored"    // hidden, can be restored
)

// Presence states, in boot order.
const (
	StateUnknown   = "unknown"
	StateOffline   = "offline"
	StateReachable = "reachable" // answers ping/TCP but its service is not up yet
	StateReady     = "ready"     // service answers and stayed up for the settle time
	StatePaused    = "paused"    // presence checks are off for this device
)

// ProbeConfig describes one network check.
type ProbeConfig struct {
	Method string `json:"method"` // none | ping | tcp | http
	Port   int    `json:"port,omitempty"`
	URL    string `json:"url,omitempty"`
}

// PresenceConfig is how a device is checked.
type PresenceConfig struct {
	Reach           ProbeConfig `json:"reach"`
	Ready           ProbeConfig `json:"ready"`
	SettleSeconds   int         `json:"settleSeconds"`
	IntervalSeconds int         `json:"intervalSeconds"`
}

// Normalize fills defaults and clamps values.
func (p *PresenceConfig) Normalize() {
	if p.Reach.Method == "" {
		p.Reach.Method = "ping"
	}
	if p.Ready.Method == "" {
		p.Ready.Method = "none"
	}
	if p.SettleSeconds < 0 {
		p.SettleSeconds = 0
	}
	if p.SettleSeconds > 3600 {
		p.SettleSeconds = 3600
	}
	if p.IntervalSeconds < 10 {
		p.IntervalSeconds = 30
	}
	if p.IntervalSeconds > 3600 {
		p.IntervalSeconds = 3600
	}
}

// Device is anything Homewarden watches: a host, a Pi, a VM, a container.
type Device struct {
	ID         int64           `json:"id"`
	Name       string          `json:"name"`
	Kind       string          `json:"kind"`
	Host       string          `json:"host"`
	MAC        string          `json:"mac"`
	Vendor     string          `json:"vendor,omitempty"` // filled from the MAC by the API
	ParentID   *int64          `json:"parentId"`
	GroupName  string          `json:"groupName"`
	Status     string          `json:"status"`
	Source     string          `json:"source"`
	ExternalID string          `json:"externalId"`
	Presence   PresenceConfig  `json:"presence"`
	Config     json.RawMessage `json:"config"`
	State      string          `json:"state"`
	StateSince *int64          `json:"stateSince"`
	LastSeen   *int64          `json:"lastSeen"`
	RTTms      *float64        `json:"rttMs"`
	Notes      string          `json:"notes"`
	CreatedAt  int64           `json:"createdAt"`
}

const deviceCols = "id, name, kind, host, mac, parent_id, group_name, status, source, external_id, presence, config, state, state_since, last_seen, rtt_ms, notes, created_at"

func scanDevice(row interface{ Scan(...any) error }) (Device, error) {
	var d Device
	var presence, config string
	err := row.Scan(&d.ID, &d.Name, &d.Kind, &d.Host, &d.MAC, &d.ParentID, &d.GroupName, &d.Status, &d.Source, &d.ExternalID,
		&presence, &config, &d.State, &d.StateSince, &d.LastSeen, &d.RTTms, &d.Notes, &d.CreatedAt)
	if err != nil {
		return d, err
	}
	_ = json.Unmarshal([]byte(presence), &d.Presence)
	d.Presence.Normalize()
	if config == "" {
		config = "{}"
	}
	d.Config = json.RawMessage(config)
	return d, nil
}

// Devices lists devices with a status ("" = all), ordered by name.
func (s *Store) Devices(ctx context.Context, status string) ([]Device, error) {
	q := "SELECT " + deviceCols + " FROM devices"
	args := []any{}
	if status != "" {
		q += " WHERE status = ?"
		args = append(args, status)
	}
	q += " ORDER BY name COLLATE NOCASE"
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Device{}
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Device returns one device.
func (s *Store) Device(ctx context.Context, id int64) (Device, error) {
	d, err := scanDevice(s.DB.QueryRowContext(ctx, "SELECT "+deviceCols+" FROM devices WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// DeviceByExternal finds a device by its discovery key.
func (s *Store) DeviceByExternal(ctx context.Context, source, externalID string) (Device, error) {
	d, err := scanDevice(s.DB.QueryRowContext(ctx, "SELECT "+deviceCols+" FROM devices WHERE source = ? AND external_id = ?", source, externalID))
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// HostKnown reports whether any device already uses this host address.
func (s *Store) HostKnown(ctx context.Context, host string) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM devices WHERE lower(host) = ?", strings.ToLower(host)).Scan(&n)
	return n > 0, err
}

// CreateDevice inserts a device and returns it.
func (s *Store) CreateDevice(ctx context.Context, d Device) (Device, error) {
	d.Presence.Normalize()
	p, _ := json.Marshal(d.Presence)
	if len(d.Config) == 0 {
		d.Config = json.RawMessage("{}")
	}
	if d.Status == "" {
		d.Status = StatusActive
	}
	if d.Source == "" {
		d.Source = "manual"
	}
	if d.Kind == "" {
		d.Kind = "generic"
	}
	res, err := s.DB.ExecContext(ctx, `INSERT INTO devices(name, kind, host, mac, parent_id, group_name, status, source, external_id, presence, config, notes, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.Name, d.Kind, d.Host, d.MAC, d.ParentID, d.GroupName, d.Status, d.Source, d.ExternalID, string(p), string(d.Config), d.Notes, now())
	if err != nil {
		return Device{}, err
	}
	id, _ := res.LastInsertId()
	return s.Device(ctx, id)
}

// UpdateDevice stores the editable fields.
func (s *Store) UpdateDevice(ctx context.Context, d Device) error {
	d.Presence.Normalize()
	p, _ := json.Marshal(d.Presence)
	if len(d.Config) == 0 {
		d.Config = json.RawMessage("{}")
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE devices SET name = ?, kind = ?, host = ?, mac = ?, parent_id = ?, group_name = ?, presence = ?, config = ?, notes = ? WHERE id = ?`,
		d.Name, d.Kind, d.Host, d.MAC, d.ParentID, d.GroupName, string(p), string(d.Config), d.Notes, d.ID)
	return err
}

// SetDeviceStatus moves a device between active, discovered and ignored.
func (s *Store) SetDeviceStatus(ctx context.Context, id int64, status string) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE devices SET status = ? WHERE id = ?", status, id)
	return err
}

// DeleteDevice removes a device and its history.
func (s *Store) DeleteDevice(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM devices WHERE id = ?", id)
	return err
}

// RecordSeen updates last-seen and round-trip time without a state change.
func (s *Store) RecordSeen(ctx context.Context, id int64, at int64, rtt *float64) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE devices SET last_seen = ?, rtt_ms = ? WHERE id = ?", at, rtt, id)
	return err
}

// SetDeviceState records a presence state change and its history event.
func (s *Store) SetDeviceState(ctx context.Context, id int64, state string, at int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "UPDATE devices SET state = ?, state_since = ? WHERE id = ?", state, at, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO presence_events(device_id, at, state) VALUES(?, ?, ?)", id, at, state); err != nil {
		return err
	}
	return tx.Commit()
}

// PresenceEvent is one state change.
type PresenceEvent struct {
	At    int64  `json:"at"`
	State string `json:"state"`
}

// PresenceHistory returns the state changes since a time, plus the last one before
// it so the caller knows the state at the start of the window.
func (s *Store) PresenceHistory(ctx context.Context, id, since int64) ([]PresenceEvent, error) {
	out := []PresenceEvent{}
	var prev PresenceEvent
	err := s.DB.QueryRowContext(ctx, "SELECT at, state FROM presence_events WHERE device_id = ? AND at < ? ORDER BY at DESC LIMIT 1", id, since).Scan(&prev.At, &prev.State)
	if err == nil {
		out = append(out, prev)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT at, state FROM presence_events WHERE device_id = ? AND at >= ? ORDER BY at", id, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var e PresenceEvent
		if err := rows.Scan(&e.At, &e.State); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PrunePresence deletes history older than a time.
func (s *Store) PrunePresence(ctx context.Context, before int64) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM presence_events WHERE at < ?", before)
	return err
}

// DeviceByMACOrHost finds a device with the same MAC address, or else the same
// address, to merge discoveries from different sources into one device.
func (s *Store) DeviceByMACOrHost(ctx context.Context, mac, host string) (Device, error) {
	if mac = strings.ToLower(strings.TrimSpace(mac)); mac != "" {
		d, err := scanDevice(s.DB.QueryRowContext(ctx, "SELECT "+deviceCols+" FROM devices WHERE lower(mac) = ? ORDER BY id LIMIT 1", mac))
		if err == nil {
			return d, nil
		}
	}
	if host = strings.ToLower(strings.TrimSpace(host)); host != "" {
		d, err := scanDevice(s.DB.QueryRowContext(ctx, "SELECT "+deviceCols+" FROM devices WHERE lower(host) = ? ORDER BY id LIMIT 1", host))
		if err == nil {
			return d, nil
		}
	}
	return Device{}, ErrNotFound
}

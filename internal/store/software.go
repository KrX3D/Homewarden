package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// InstalledIntegration is a row of the integrations table.
type InstalledIntegration struct {
	ID          string `json:"id"`
	Version     string `json:"version"`
	Source      string `json:"source"`
	Enabled     bool   `json:"enabled"`
	InstalledAt int64  `json:"installedAt"`
}

// InstalledIntegrations lists installed integrations.
func (s *Store) InstalledIntegrations(ctx context.Context) ([]InstalledIntegration, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT id, version, source, enabled, installed_at FROM integrations ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InstalledIntegration{}
	for rows.Next() {
		var i InstalledIntegration
		if err := rows.Scan(&i.ID, &i.Version, &i.Source, &i.Enabled, &i.InstalledAt); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// SaveIntegration records an installed (or updated) integration.
func (s *Store) SaveIntegration(ctx context.Context, i InstalledIntegration) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO integrations(id, version, source, enabled, installed_at) VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET version = excluded.version, source = excluded.source, enabled = excluded.enabled, installed_at = excluded.installed_at`,
		i.ID, i.Version, i.Source, i.Enabled, now())
	return err
}

// RemoveIntegration forgets an installed integration.
func (s *Store) RemoveIntegration(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM integrations WHERE id = ?", id)
	return err
}

// Component is one updatable part of a device: the OS, a package, a container, a
// plugin.
type Component struct {
	DeviceID        int64           `json:"deviceId"`
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Group           string          `json:"group"`
	Installed       string          `json:"installed"`
	Available       string          `json:"available"`
	UpdateAvailable bool            `json:"updateAvailable"`
	Security        bool            `json:"security"`
	Updatable       bool            `json:"updatable"`
	Meta            json.RawMessage `json:"meta"`
	UpdatedAt       int64           `json:"updatedAt"`
}

const compCols = "device_id, component_id, name, grp, installed, available, update_available, security, updatable, meta, updated_at"

func scanComp(row interface{ Scan(...any) error }) (Component, error) {
	var c Component
	var meta string
	err := row.Scan(&c.DeviceID, &c.ID, &c.Name, &c.Group, &c.Installed, &c.Available, &c.UpdateAvailable, &c.Security, &c.Updatable, &meta, &c.UpdatedAt)
	c.Meta = json.RawMessage(meta)
	return c, err
}

func queryComps(ctx context.Context, db *sql.DB, q string, args ...any) ([]Component, error) {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Component{}
	for rows.Next() {
		c, err := scanComp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Components returns a device's components, updates first.
func (s *Store) Components(ctx context.Context, deviceID int64) ([]Component, error) {
	return queryComps(ctx, s.DB, "SELECT "+compCols+" FROM components WHERE device_id = ? ORDER BY update_available DESC, security DESC, grp, name COLLATE NOCASE", deviceID)
}

// PendingUpdates returns every component with an update across active devices.
func (s *Store) PendingUpdates(ctx context.Context) ([]Component, error) {
	return queryComps(ctx, s.DB, `SELECT `+compCols+` FROM components c WHERE update_available = 1
		AND EXISTS (SELECT 1 FROM devices d WHERE d.id = c.device_id AND d.status = 'active')
		ORDER BY device_id, security DESC, name COLLATE NOCASE`)
}

// ReplaceComponents stores a fresh scan result and records version changes in the
// history. It returns how many components changed version.
func (s *Store) ReplaceComponents(ctx context.Context, deviceID int64, comps []Component) (int, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	old := map[string]string{}
	rows, err := tx.QueryContext(ctx, "SELECT component_id, installed FROM components WHERE device_id = ?", deviceID)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var id, v string
		rows.Scan(&id, &v)
		old[id] = v
	}
	rows.Close()
	if _, err := tx.ExecContext(ctx, "DELETE FROM components WHERE device_id = ?", deviceID); err != nil {
		return 0, err
	}
	t := now()
	changed := 0
	for _, c := range comps {
		if len(c.Meta) == 0 {
			c.Meta = json.RawMessage("{}")
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO components(`+compCols+`) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			deviceID, c.ID, c.Name, c.Group, c.Installed, c.Available, c.UpdateAvailable, c.Security, c.Updatable, string(c.Meta), t); err != nil {
			return 0, err
		}
		if prev, ok := old[c.ID]; ok && prev != c.Installed && c.Installed != "" {
			changed++
			if _, err := tx.ExecContext(ctx, "INSERT INTO version_history(device_id, component_id, name, old_version, new_version, at) VALUES(?, ?, ?, ?, ?, ?)",
				deviceID, c.ID, c.Name, prev, c.Installed, t); err != nil {
				return 0, err
			}
		}
	}
	return changed, tx.Commit()
}

// AddVersionChange records one change (used after updates, when the updated
// component may no longer be listed by the next scan).
func (s *Store) AddVersionChange(ctx context.Context, deviceID int64, v VersionChange) error {
	_, err := s.DB.ExecContext(ctx, "INSERT INTO version_history(device_id, component_id, name, old_version, new_version, at) VALUES(?, ?, ?, ?, ?, ?)",
		deviceID, v.ComponentID, v.Name, v.OldVersion, v.NewVersion, now())
	return err
}

// VersionChange is one entry of the version history.
type VersionChange struct {
	ComponentID string `json:"componentId"`
	Name        string `json:"name"`
	OldVersion  string `json:"oldVersion"`
	NewVersion  string `json:"newVersion"`
	At          int64  `json:"at"`
}

// VersionHistory returns a device's version changes, newest first.
func (s *Store) VersionHistory(ctx context.Context, deviceID int64, limit int) ([]VersionChange, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT component_id, name, old_version, new_version, at FROM version_history WHERE device_id = ? ORDER BY at DESC, id DESC LIMIT ?", deviceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []VersionChange{}
	for rows.Next() {
		var v VersionChange
		if err := rows.Scan(&v.ComponentID, &v.Name, &v.OldVersion, &v.NewVersion, &v.At); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// PruneHistory deletes version history and finished jobs older than a time.
func (s *Store) PruneHistory(ctx context.Context, before int64) error {
	if _, err := s.DB.ExecContext(ctx, "DELETE FROM version_history WHERE at < ?", before); err != nil {
		return err
	}
	_, err := s.DB.ExecContext(ctx, "DELETE FROM jobs WHERE created_at < ? AND status NOT IN ('queued','running')", before)
	return err
}

// Facts is what the last scan learned about a device.
type Facts struct {
	Facts     json.RawMessage `json:"facts"`
	ScannedAt int64           `json:"scannedAt"`
	Error     string          `json:"error"`
}

// SetFacts stores the outcome of a scan.
func (s *Store) SetFacts(ctx context.Context, deviceID int64, facts json.RawMessage, scanErr string) error {
	if len(facts) == 0 {
		facts = json.RawMessage("{}")
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO device_facts(device_id, facts, scanned_at, error) VALUES(?, ?, ?, ?)
		ON CONFLICT(device_id) DO UPDATE SET facts = CASE WHEN excluded.error = '' THEN excluded.facts ELSE device_facts.facts END,
		scanned_at = excluded.scanned_at, error = excluded.error`, deviceID, string(facts), now(), scanErr)
	return err
}

// DeviceFacts returns the last scan outcome.
func (s *Store) DeviceFacts(ctx context.Context, deviceID int64) (Facts, error) {
	var f Facts
	var facts string
	err := s.DB.QueryRowContext(ctx, "SELECT facts, scanned_at, error FROM device_facts WHERE device_id = ?", deviceID).Scan(&facts, &f.ScannedAt, &f.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return Facts{Facts: json.RawMessage("{}")}, nil
	}
	f.Facts = json.RawMessage(facts)
	return f, err
}

// UpdateCounts returns pending update and security counts per device.
func (s *Store) UpdateCounts(ctx context.Context) (map[int64][2]int, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT device_id, SUM(update_available), SUM(update_available AND security) FROM components GROUP BY device_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][2]int{}
	for rows.Next() {
		var id int64
		var n, sec int
		if err := rows.Scan(&id, &n, &sec); err != nil {
			return nil, err
		}
		out[id] = [2]int{n, sec}
	}
	return out, rows.Err()
}

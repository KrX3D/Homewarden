package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// BackupTarget is where backups are written: an SMB share or a mounted folder.
type BackupTarget struct {
	ID        int64           `json:"id"`
	Name      string          `json:"name"`
	Type      string          `json:"type"` // smb | local
	Config    json.RawMessage `json:"config"`
	CreatedAt int64           `json:"createdAt"`
}

// BackupTargets lists targets.
func (s *Store) BackupTargets(ctx context.Context) ([]BackupTarget, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT id, name, type, config, created_at FROM backup_targets ORDER BY name COLLATE NOCASE")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BackupTarget{}
	for rows.Next() {
		var t BackupTarget
		var cfg string
		if err := rows.Scan(&t.ID, &t.Name, &t.Type, &cfg, &t.CreatedAt); err != nil {
			return nil, err
		}
		t.Config = json.RawMessage(cfg)
		out = append(out, t)
	}
	return out, rows.Err()
}

// BackupTarget returns one target.
func (s *Store) BackupTarget(ctx context.Context, id int64) (BackupTarget, error) {
	var t BackupTarget
	var cfg string
	err := s.DB.QueryRowContext(ctx, "SELECT id, name, type, config, created_at FROM backup_targets WHERE id = ?", id).Scan(&t.ID, &t.Name, &t.Type, &cfg, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	t.Config = json.RawMessage(cfg)
	return t, err
}

// SaveBackupTarget inserts (ID 0) or updates a target.
func (s *Store) SaveBackupTarget(ctx context.Context, t BackupTarget) (BackupTarget, error) {
	if t.ID == 0 {
		res, err := s.DB.ExecContext(ctx, "INSERT INTO backup_targets(name, type, config, created_at) VALUES(?, ?, ?, ?)", t.Name, t.Type, string(t.Config), now())
		if err != nil {
			return t, err
		}
		t.ID, _ = res.LastInsertId()
	} else if _, err := s.DB.ExecContext(ctx, "UPDATE backup_targets SET name = ?, type = ?, config = ? WHERE id = ?", t.Name, t.Type, string(t.Config), t.ID); err != nil {
		return t, err
	}
	return s.BackupTarget(ctx, t.ID)
}

// DeleteBackupTarget removes a target (its backup rows keep their history).
func (s *Store) DeleteBackupTarget(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM backup_targets WHERE id = ?", id)
	return err
}

// Backup is one stored backup file.
type Backup struct {
	ID       int64  `json:"id"`
	DeviceID int64  `json:"deviceId"`
	TargetID *int64 `json:"targetId"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	At       int64  `json:"at"`
	JobID    *int64 `json:"jobId"`
}

const backupCols = "id, device_id, target_id, name, path, size, at, job_id"

func scanBackup(row interface{ Scan(...any) error }) (Backup, error) {
	var b Backup
	err := row.Scan(&b.ID, &b.DeviceID, &b.TargetID, &b.Name, &b.Path, &b.Size, &b.At, &b.JobID)
	return b, err
}

// AddBackup records a stored backup.
func (s *Store) AddBackup(ctx context.Context, b Backup) error {
	_, err := s.DB.ExecContext(ctx, "INSERT INTO backups(device_id, target_id, name, path, size, at, job_id) VALUES(?, ?, ?, ?, ?, ?, ?)",
		b.DeviceID, b.TargetID, b.Name, b.Path, b.Size, now(), b.JobID)
	return err
}

// Backups lists a device's backups, newest first (deviceID 0 = all).
func (s *Store) Backups(ctx context.Context, deviceID int64) ([]Backup, error) {
	q := "SELECT " + backupCols + " FROM backups"
	args := []any{}
	if deviceID > 0 {
		q += " WHERE device_id = ?"
		args = append(args, deviceID)
	}
	rows, err := s.DB.QueryContext(ctx, q+" ORDER BY at DESC, id DESC", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Backup{}
	for rows.Next() {
		b, err := scanBackup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Backup returns one backup.
func (s *Store) Backup(ctx context.Context, id int64) (Backup, error) {
	b, err := scanBackup(s.DB.QueryRowContext(ctx, "SELECT "+backupCols+" FROM backups WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return b, ErrNotFound
	}
	return b, err
}

// DeleteBackup removes a backup row.
func (s *Store) DeleteBackup(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM backups WHERE id = ?", id)
	return err
}

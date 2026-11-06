package store

import (
	"context"
	"database/sql"
	"errors"
)

// Agent is the record of a device's agent.
type Agent struct {
	DeviceID      int64  `json:"deviceId"`
	KeyHash       string `json:"-"`
	EnrollHash    string `json:"-"`
	EnrollExpires int64  `json:"enrollExpires"`
	Hostname      string `json:"hostname"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	Version       string `json:"version"`
	LastSeen      *int64 `json:"lastSeen"`
	Enrolled      bool   `json:"enrolled"`
}

const agentCols = "device_id, key_hash, enroll_hash, enroll_expires, hostname, os, arch, version, last_seen"

func scanAgent(row interface{ Scan(...any) error }) (Agent, error) {
	var a Agent
	err := row.Scan(&a.DeviceID, &a.KeyHash, &a.EnrollHash, &a.EnrollExpires, &a.Hostname, &a.OS, &a.Arch, &a.Version, &a.LastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	a.Enrolled = a.KeyHash != ""
	return a, err
}

// AgentFor returns a device's agent record.
func (s *Store) AgentFor(ctx context.Context, deviceID int64) (Agent, error) {
	return scanAgent(s.DB.QueryRowContext(ctx, "SELECT "+agentCols+" FROM agents WHERE device_id = ?", deviceID))
}

// AgentByKey finds an enrolled agent by its key hash.
func (s *Store) AgentByKey(ctx context.Context, hash string) (Agent, error) {
	return scanAgent(s.DB.QueryRowContext(ctx, "SELECT "+agentCols+" FROM agents WHERE key_hash = ? AND key_hash <> ''", hash))
}

// AgentByEnroll finds a pending enrollment by its token hash.
func (s *Store) AgentByEnroll(ctx context.Context, hash string) (Agent, error) {
	return scanAgent(s.DB.QueryRowContext(ctx, "SELECT "+agentCols+" FROM agents WHERE enroll_hash = ? AND enroll_hash <> '' AND enroll_expires > ?", hash, now()))
}

// StartEnrollment stores a one-time enrollment token for a device. An existing agent
// keeps working until the new one enrolls.
func (s *Store) StartEnrollment(ctx context.Context, deviceID int64, hash string, expires int64) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO agents(device_id, enroll_hash, enroll_expires) VALUES(?, ?, ?)
		ON CONFLICT(device_id) DO UPDATE SET enroll_hash = excluded.enroll_hash, enroll_expires = excluded.enroll_expires`, deviceID, hash, expires)
	return err
}

// CompleteEnrollment swaps the enrollment token for the agent key.
func (s *Store) CompleteEnrollment(ctx context.Context, deviceID int64, keyHash, hostname, os, arch, version string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE agents SET key_hash = ?, enroll_hash = '', enroll_expires = 0, hostname = ?, os = ?, arch = ?, version = ?, last_seen = ?
		WHERE device_id = ?`, keyHash, hostname, os, arch, version, now(), deviceID)
	return err
}

// TouchAgent records a heartbeat.
func (s *Store) TouchAgent(ctx context.Context, deviceID int64, hostname, os, arch, version string) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE agents SET hostname = ?, os = ?, arch = ?, version = ?, last_seen = ? WHERE device_id = ?",
		hostname, os, arch, version, now(), deviceID)
	return err
}

// DeleteAgent revokes a device's agent.
func (s *Store) DeleteAgent(ctx context.Context, deviceID int64) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM agents WHERE device_id = ?", deviceID)
	return err
}

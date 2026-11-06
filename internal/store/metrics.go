package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// MetricPoint is one stored sample (data as returned by the integration's metrics()).
type MetricPoint struct {
	At   int64           `json:"at"`
	Data json.RawMessage `json:"data"`
}

// AddMetrics stores a sample.
func (s *Store) AddMetrics(ctx context.Context, deviceID int64, data json.RawMessage) error {
	_, err := s.DB.ExecContext(ctx, "INSERT INTO metrics(device_id, at, data) VALUES(?, ?, ?)", deviceID, now(), string(data))
	return err
}

// Metrics returns samples since a time, oldest first.
func (s *Store) Metrics(ctx context.Context, deviceID, since int64) ([]MetricPoint, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT at, data FROM metrics WHERE device_id = ? AND at >= ? ORDER BY at", deviceID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MetricPoint{}
	for rows.Next() {
		var p MetricPoint
		var d string
		if err := rows.Scan(&p.At, &d); err != nil {
			return nil, err
		}
		p.Data = json.RawMessage(d)
		out = append(out, p)
	}
	return out, rows.Err()
}

// LatestMetrics returns the newest sample, if any.
func (s *Store) LatestMetrics(ctx context.Context, deviceID int64) (MetricPoint, error) {
	var p MetricPoint
	var d string
	err := s.DB.QueryRowContext(ctx, "SELECT at, data FROM metrics WHERE device_id = ? ORDER BY at DESC LIMIT 1", deviceID).Scan(&p.At, &d)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	p.Data = json.RawMessage(d)
	return p, err
}

// PruneMetrics removes samples older than a time.
func (s *Store) PruneMetrics(ctx context.Context, before int64) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM metrics WHERE at < ?", before)
	return err
}

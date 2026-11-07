package store

import (
	"context"
	"encoding/json"
)

// NetworkInfo is what one device (e.g. a UniFi console) reports about how another
// device is connected. Data is integration-defined; the UI knows common keys.
type NetworkInfo struct {
	SourceID   int64           `json:"sourceId"`
	SourceName string          `json:"sourceName"`
	Data       json.RawMessage `json:"data"`
	UpdatedAt  int64           `json:"updatedAt"`
}

// SetNetworkInfo stores the report of source about device.
func (s *Store) SetNetworkInfo(ctx context.Context, deviceID, sourceID int64, data json.RawMessage) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO network_info(device_id, source_id, data, updated_at) VALUES(?, ?, ?, ?)
		ON CONFLICT(device_id, source_id) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
		deviceID, sourceID, string(data), now())
	return err
}

// NetworkInfoFor returns every report about a device, newest first.
func (s *Store) NetworkInfoFor(ctx context.Context, deviceID int64) ([]NetworkInfo, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT n.source_id, d.name, n.data, n.updated_at FROM network_info n
		JOIN devices d ON d.id = n.source_id WHERE n.device_id = ? ORDER BY n.updated_at DESC`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NetworkInfo{}
	for rows.Next() {
		var n NetworkInfo
		var data string
		if err := rows.Scan(&n.SourceID, &n.SourceName, &data, &n.UpdatedAt); err != nil {
			return nil, err
		}
		n.Data = json.RawMessage(data)
		out = append(out, n)
	}
	return out, rows.Err()
}

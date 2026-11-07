package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// Job statuses.
const (
	JobQueued    = "queued"
	JobRunning   = "running"
	JobSuccess   = "success"
	JobFailed    = "failed"
	JobBlocked   = "blocked" // a busy guard stopped it
	JobCancelled = "cancelled"
)

// Job is one unit of background work (scan, update, backup).
type Job struct {
	ID         int64           `json:"id"`
	Type       string          `json:"type"`
	DeviceID   *int64          `json:"deviceId"`
	DeviceName string          `json:"deviceName"`
	Title      string          `json:"title"`
	Params     json.RawMessage `json:"params"`
	Status     string          `json:"status"`
	CreatedBy  string          `json:"createdBy"`
	CreatedAt  int64           `json:"createdAt"`
	StartedAt  *int64          `json:"startedAt"`
	FinishedAt *int64          `json:"finishedAt"`
	Log        string          `json:"log,omitempty"`
	Result     json.RawMessage `json:"result"`
}

const jobCols = "id, type, device_id, device_name, title, params, status, created_by, created_at, started_at, finished_at, result"

func scanJob(row interface{ Scan(...any) error }, withLog bool) (Job, error) {
	var j Job
	var params, result string
	dest := []any{&j.ID, &j.Type, &j.DeviceID, &j.DeviceName, &j.Title, &params, &j.Status, &j.CreatedBy, &j.CreatedAt, &j.StartedAt, &j.FinishedAt, &result}
	if withLog {
		dest = append(dest, &j.Log)
	}
	if err := row.Scan(dest...); err != nil {
		return j, err
	}
	j.Params, j.Result = json.RawMessage(params), json.RawMessage(result)
	return j, nil
}

// CreateJob queues a job.
func (s *Store) CreateJob(ctx context.Context, j Job) (Job, error) {
	if len(j.Params) == 0 {
		j.Params = json.RawMessage("{}")
	}
	res, err := s.DB.ExecContext(ctx, "INSERT INTO jobs(type, device_id, device_name, title, params, status, created_by, created_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?)",
		j.Type, j.DeviceID, j.DeviceName, j.Title, string(j.Params), JobQueued, j.CreatedBy, now())
	if err != nil {
		return Job{}, err
	}
	id, _ := res.LastInsertId()
	return s.Job(ctx, id)
}

// Job returns one job with its log.
func (s *Store) Job(ctx context.Context, id int64) (Job, error) {
	j, err := scanJob(s.DB.QueryRowContext(ctx, "SELECT "+jobCols+", log FROM jobs WHERE id = ?", id), true)
	if errors.Is(err, sql.ErrNoRows) {
		return j, ErrNotFound
	}
	return j, err
}

// Jobs lists recent jobs without logs, optionally for one device.
func (s *Store) Jobs(ctx context.Context, deviceID int64, limit int) ([]Job, error) {
	return s.JobsBefore(ctx, deviceID, 0, limit)
}

// JobsBefore pages backwards through the job list: jobs with an id below before
// (0 = from the newest), newest first.
func (s *Store) JobsBefore(ctx context.Context, deviceID, before int64, limit int) ([]Job, error) {
	q := "SELECT " + jobCols + " FROM jobs WHERE 1=1"
	args := []any{}
	if deviceID > 0 {
		q += " AND device_id = ?"
		args = append(args, deviceID)
	}
	if before > 0 {
		q += " AND id < ?"
		args = append(args, before)
	}
	q += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		j, err := scanJob(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// StartJob marks a job running.
func (s *Store) StartJob(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE jobs SET status = ?, started_at = ? WHERE id = ?", JobRunning, now(), id)
	return err
}

// AppendJobLog adds text to a job's log.
func (s *Store) AppendJobLog(ctx context.Context, id int64, text string) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE jobs SET log = log || ? WHERE id = ?", text, id)
	return err
}

// FinishJob records the outcome.
func (s *Store) FinishJob(ctx context.Context, id int64, status string, result json.RawMessage) error {
	if len(result) == 0 {
		result = json.RawMessage("{}")
	}
	_, err := s.DB.ExecContext(ctx, "UPDATE jobs SET status = ?, finished_at = ?, result = ? WHERE id = ?", status, now(), string(result), id)
	return err
}

// FailStaleJobs marks jobs left queued or running by a previous process as failed.
func (s *Store) FailStaleJobs(ctx context.Context) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE jobs SET status = ?, finished_at = ?, log = log || ? WHERE status IN (?, ?)",
		JobFailed, now(), "\n[Homewarden restarted while this job was pending]\n", JobQueued, JobRunning)
	return err
}

// LastJob returns the newest job of a type for a device.
func (s *Store) LastJob(ctx context.Context, deviceID int64, typ string) (Job, error) {
	j, err := scanJob(s.DB.QueryRowContext(ctx, "SELECT "+jobCols+" FROM jobs WHERE device_id = ? AND type = ? ORDER BY id DESC LIMIT 1", deviceID, typ), false)
	if errors.Is(err, sql.ErrNoRows) {
		return j, ErrNotFound
	}
	return j, err
}

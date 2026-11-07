package jobs

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/KrX3D/Homewarden/internal/backup"
	"github.com/KrX3D/Homewarden/internal/integrations"
	"github.com/KrX3D/Homewarden/internal/store"
)

// RestoreParams are the parameters of a restore job.
type RestoreParams struct {
	BackupID int64 `json:"backupId"`
	Force    bool  `json:"force"`
}

// RegisterRestore adds the restore job type.
func (s *Software) RegisterRestore() { s.Runner.Handle("restore", s.restoreJob) }

// EnqueueRestore queues restoring a stored backup onto a device (the one it came
// from, or a replacement).
func (s *Software) EnqueueRestore(ctx context.Context, d store.Device, backupID int64, force bool, by string) (store.Job, error) {
	b, _ := json.Marshal(RestoreParams{BackupID: backupID, Force: force})
	id := d.ID
	return s.Runner.Enqueue(ctx, store.Job{Type: "restore", DeviceID: &id, DeviceName: d.Name, Title: "Restore backup to " + d.Name, Params: b, CreatedBy: by})
}

func (s *Software) restoreJob(ctx context.Context, run *Run) (string, any, error) {
	d, err := s.St.Device(ctx, *run.Job.DeviceID)
	if err != nil {
		return store.JobFailed, nil, err
	}
	in, err := s.integration(d)
	if err != nil {
		return store.JobFailed, nil, err
	}
	if !in.Has("restore") {
		return store.JobFailed, nil, errors.New("this integration cannot restore backups")
	}
	var p RestoreParams
	if err := run.Params(&p); err != nil {
		return store.JobFailed, nil, err
	}
	b, err := s.St.Backup(ctx, p.BackupID)
	if err != nil || b.TargetID == nil {
		return store.JobFailed, nil, errors.New("backup not found")
	}
	if in.Has("busy") && !p.Force {
		raw, err := s.Host.Call(ctx, in, d, "busy", nil, func(l string) { run.Log("%s", l) })
		var st struct {
			Busy   bool   `json:"busy"`
			Reason string `json:"reason"`
		}
		if err == nil && json.Unmarshal(raw, &st) == nil && st.Busy {
			run.Log("Not restoring: %s", st.Reason)
			return store.JobBlocked, map[string]string{"reason": st.Reason}, nil
		}
	}
	raw, err := s.Host.Call(ctx, in, d, "restore", nil, func(l string) { run.Log("%s", l) })
	if err != nil {
		return store.JobFailed, nil, err
	}
	var out struct {
		Upload struct {
			URL     string            `json:"url"`
			Field   string            `json:"field"`
			Headers map[string]string `json:"headers"`
		} `json:"upload"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Upload.URL == "" {
		return store.JobFailed, nil, errors.New("restore() did not return an upload")
	}
	u, err := url.Parse(out.Upload.URL)
	if err != nil || !integrations.AllowedHost(in, s.St.WithDefaults(ctx, d), u.Hostname()) {
		return store.JobFailed, nil, errors.New("restore upload points to a host this integration may not reach")
	}
	if out.Upload.Field == "" {
		out.Upload.Field = "file"
	}
	tgtRow, err := s.St.BackupTarget(ctx, *b.TargetID)
	if err != nil {
		return store.JobFailed, nil, err
	}
	tgt, err := backup.Open(ctx, s.St, s.Host.Vault, tgtRow)
	if err != nil {
		return store.JobFailed, nil, err
	}
	defer tgt.Close()
	src, err := tgt.Open(ctx, b.Path)
	if err != nil {
		return store.JobFailed, nil, fmt.Errorf("read backup: %w", err)
	}
	defer src.Close()

	// Stream the file as multipart with an exact Content-Length: some servers do not
	// accept chunked uploads, and the size is known from when the backup was stored.
	var head bytes.Buffer
	mw := multipart.NewWriter(&head)
	if _, err := mw.CreateFormFile(out.Upload.Field, path.Base(b.Path)); err != nil {
		return store.JobFailed, nil, err
	}
	tail := "\r\n--" + mw.Boundary() + "--\r\n"
	body := io.MultiReader(bytes.NewReader(head.Bytes()), io.LimitReader(src, b.Size), strings.NewReader(tail))
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, out.Upload.URL, body)
	req.ContentLength = int64(head.Len()) + b.Size + int64(len(tail))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	for k, v := range out.Upload.Headers {
		req.Header.Set(k, v)
	}
	insecure, _ := integrations.Settings(s.St.WithDefaults(ctx, d))["insecure"].(bool)
	client := &http.Client{Timeout: 30 * time.Minute, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure}}}
	run.Log("Uploading %s (%d MB) to %s", b.Name, b.Size>>20, u.Host)
	resp, err := client.Do(req)
	if err != nil {
		return store.JobFailed, nil, fmt.Errorf("upload: %w", err)
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return store.JobFailed, nil, fmt.Errorf("the device rejected the backup: HTTP %d %s", resp.StatusCode, respBody)
	}
	run.Log("The device accepted the backup and is restoring it. It may restart; the next scan shows the restored state.")
	return store.JobSuccess, map[string]any{"backup": b.Name}, nil
}

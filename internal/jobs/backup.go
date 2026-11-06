package jobs

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/KrX3D/Homewarden/internal/backup"
	"github.com/KrX3D/Homewarden/internal/integrations"
	"github.com/KrX3D/Homewarden/internal/store"
)

// BackupSettings is the "backup" object of a device config.
type BackupSettings struct {
	TargetID     int64 `json:"targetId"`
	BeforeUpdate bool  `json:"beforeUpdate"`
	Keep         int   `json:"keep"`
}

// Guard is one user-defined busy rule from the "guards" list of a device config.
type Guard struct {
	Name     string `json:"name"`
	Type     string `json:"type"` // ssh | http
	Command  string `json:"command,omitempty"`
	URL      string `json:"url,omitempty"`
	Contains string `json:"contains,omitempty"`
	Mode     string `json:"mode"` // block | warn
}

type deviceExtras struct {
	Backup BackupSettings `json:"backup"`
	Guards []Guard        `json:"guards"`
}

func extras(d store.Device) deviceExtras {
	var e deviceExtras
	_ = json.Unmarshal(d.Config, &e)
	if e.Backup.Keep <= 0 {
		e.Backup.Keep = 5
	}
	return e
}

// RegisterBackup adds the backup job type.
func (s *Software) RegisterBackup() { s.Runner.Handle("backup", s.backupJob) }

// EnqueueBackup queues a backup of a device.
func (s *Software) EnqueueBackup(ctx context.Context, d store.Device, by string) (store.Job, error) {
	id := d.ID
	return s.Runner.Enqueue(ctx, store.Job{Type: "backup", DeviceID: &id, DeviceName: d.Name, Title: "Backup " + d.Name, CreatedBy: by})
}

func (s *Software) backupJob(ctx context.Context, run *Run) (string, any, error) {
	d, err := s.St.Device(ctx, *run.Job.DeviceID)
	if err != nil {
		return store.JobFailed, nil, err
	}
	res, err := s.runBackup(ctx, run, d)
	if err != nil {
		return store.JobFailed, nil, err
	}
	return store.JobSuccess, res, nil
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// runBackup asks the integration for a backup, downloads it and stores it on the
// device's target, then applies retention.
func (s *Software) runBackup(ctx context.Context, run *Run, d store.Device) (map[string]any, error) {
	in, err := s.integration(d)
	if err != nil {
		return nil, err
	}
	if !in.Has("backup") {
		return nil, errors.New("this integration cannot create backups")
	}
	bs := extras(d).Backup
	if bs.TargetID == 0 {
		return nil, errors.New("no backup target set for this device (Backup card)")
	}
	tgtRow, err := s.St.BackupTarget(ctx, bs.TargetID)
	if err != nil {
		return nil, fmt.Errorf("backup target: %w", err)
	}
	raw, err := s.Host.Call(ctx, in, d, "backup", nil, func(l string) { run.Log("%s", l) })
	if err != nil {
		return nil, err
	}
	var out struct {
		Name     string `json:"name"`
		Download struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"download"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Download.URL == "" {
		return nil, errors.New("backup() did not return a download")
	}
	u, err := url.Parse(out.Download.URL)
	if err != nil || !integrations.AllowedHost(in, d, u.Hostname()) {
		return nil, errors.New("backup download points to a host this integration may not reach")
	}
	insecure, _ := integrations.Settings(d)["insecure"].(bool)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, out.Download.URL, nil)
	for k, v := range out.Download.Headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: 30 * time.Minute, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure}}}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}
	tgt, err := backup.Open(ctx, s.St, s.Host.Vault, tgtRow)
	if err != nil {
		return nil, fmt.Errorf("open target %s: %w", tgtRow.Name, err)
	}
	defer tgt.Close()
	name := unsafeName.ReplaceAllString(out.Name, "_")
	if name == "" {
		name = fmt.Sprintf("backup-%d", time.Now().Unix())
	}
	rel := unsafeName.ReplaceAllString(d.Name, "_") + "/" + time.Now().Format("2006-01-02_150405") + "_" + name
	run.Log("Copying to %s: %s", tgtRow.Name, rel)
	n, err := tgt.Put(ctx, rel, io.LimitReader(resp.Body, 50<<30))
	if err != nil {
		return nil, fmt.Errorf("write to %s: %w", tgtRow.Name, err)
	}
	tid, jid := tgtRow.ID, run.Job.ID
	if err := s.St.AddBackup(ctx, store.Backup{DeviceID: d.ID, TargetID: &tid, Name: name, Path: rel, Size: n, JobID: &jid}); err != nil {
		return nil, err
	}
	run.Log("Stored %d MB.", n>>20)

	// Retention: keep the newest N on this target.
	all, _ := s.St.Backups(ctx, d.ID)
	kept := 0
	for _, b := range all {
		if b.TargetID == nil || *b.TargetID != tid {
			continue
		}
		kept++
		if kept <= bs.Keep {
			continue
		}
		if err := tgt.Remove(ctx, b.Path); err != nil {
			run.Log("Could not remove old backup %s: %v", b.Path, err)
			continue
		}
		_ = s.St.DeleteBackup(ctx, b.ID)
		run.Log("Removed old backup %s (keeping %d).", b.Name, bs.Keep)
	}
	s.Events.Publish("device.backup", map[string]int64{"id": d.ID})
	return map[string]any{"path": rel, "size": n, "target": tgtRow.Name}, nil
}

// checkGuards runs the user's busy rules. It returns a reason when a blocking rule
// fired.
func (s *Software) checkGuards(ctx context.Context, run *Run, d store.Device) string {
	for _, g := range extras(d).Guards {
		label := g.Name
		if label == "" {
			label = g.Type
		}
		hit, err := s.evalGuard(ctx, d, g)
		if err != nil {
			run.Log("Guard %q could not be checked: %v", label, err)
			if g.Mode == "block" {
				return "guard " + label + " could not be checked"
			}
			continue
		}
		if !hit {
			continue
		}
		if g.Mode == "warn" {
			run.Log("Warning: guard %q says the device is busy. Continuing because it is set to warn.", label)
			continue
		}
		return "guard " + label + " says the device is busy"
	}
	return ""
}

func (s *Software) evalGuard(ctx context.Context, d store.Device, g Guard) (bool, error) {
	switch g.Type {
	case "ssh":
		res, err := s.Host.Conn.Exec(ctx, d, g.Command, false, 30*time.Second)
		if err != nil {
			return false, err
		}
		return res.ExitCode == 0, nil
	case "http":
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.URL, nil)
		if err != nil {
			return false, err
		}
		client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
		resp, err := client.Do(req)
		if err != nil {
			return false, err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if g.Contains == "" {
			return resp.StatusCode < 300, nil
		}
		return strings.Contains(string(body), g.Contains), nil
	}
	return false, fmt.Errorf("unknown guard type %q", g.Type)
}

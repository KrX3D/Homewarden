package api

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"

	"github.com/KrX3D/Homewarden/internal/auth"
	"github.com/KrX3D/Homewarden/internal/backup"
	"github.com/KrX3D/Homewarden/internal/jobs"
	"github.com/KrX3D/Homewarden/internal/store"
)

// AttachBackups registers backup targets, device backup settings, guards and backups.
func (s *Server) AttachBackups() {
	m := s.mux
	m.Handle("GET /api/backup-targets", s.authed(auth.PermDevicesView, s.listTargets))
	m.Handle("POST /api/backup-targets", s.authed(auth.PermBackupsManage, s.saveTarget))
	m.Handle("PUT /api/backup-targets/{id}", s.authed(auth.PermBackupsManage, s.saveTarget))
	m.Handle("DELETE /api/backup-targets/{id}", s.authed(auth.PermBackupsManage, s.deleteTarget))
	m.Handle("POST /api/backup-targets/test", s.authed(auth.PermBackupsManage, s.testTarget))

	m.Handle("GET /api/backups", s.authed(auth.PermDevicesView, s.listBackups))
	m.Handle("GET /api/backups/{id}/download", s.authed(auth.PermBackupsRun, s.downloadBackup))
	m.Handle("POST /api/devices/{id}/backup", s.authed(auth.PermBackupsRun, s.backupDevice))
	m.Handle("PUT /api/devices/{id}/backup-settings", s.authed(auth.PermBackupsManage, s.setBackupSettings))
	m.Handle("PUT /api/devices/{id}/guards", s.authed(auth.PermDevicesManage, s.setGuards))
}

func (s *Server) listTargets(w http.ResponseWriter, r *http.Request) {
	t, err := s.Store.BackupTargets(r.Context())
	if err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

type targetInput struct {
	Name   string        `json:"name"`
	Type   string        `json:"type"`
	Config backup.Config `json:"config"`
}

func (in *targetInput) toTarget() (store.BackupTarget, string) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 128 {
		return store.BackupTarget{}, "invalid_name"
	}
	switch in.Type {
	case "smb":
		if in.Config.Host == "" || in.Config.Share == "" {
			return store.BackupTarget{}, "target_incomplete"
		}
	case "local":
		if !strings.HasPrefix(in.Config.Folder, "/") {
			return store.BackupTarget{}, "target_incomplete"
		}
	default:
		return store.BackupTarget{}, "bad_request"
	}
	b, _ := json.Marshal(in.Config)
	return store.BackupTarget{Name: in.Name, Type: in.Type, Config: b}, ""
}

func (s *Server) saveTarget(w http.ResponseWriter, r *http.Request) {
	var in targetInput
	if !readJSON(w, r, &in) {
		return
	}
	t, code := in.toTarget()
	if code != "" {
		fail(w, http.StatusBadRequest, code)
		return
	}
	if r.Method == http.MethodPut {
		id, err := PathID(r)
		if err != nil {
			fail(w, http.StatusBadRequest, "bad_request")
			return
		}
		t.ID = id
	}
	out, err := s.Store.SaveBackupTarget(r.Context(), t)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			fail(w, http.StatusConflict, "target_name_taken")
			return
		}
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteTarget(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	if err := s.Store.DeleteBackupTarget(r.Context(), id); err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) testTarget(w http.ResponseWriter, r *http.Request) {
	var in targetInput
	if !readJSON(w, r, &in) {
		return
	}
	t, code := in.toTarget()
	if code != "" {
		fail(w, http.StatusBadRequest, code)
		return
	}
	tgt, err := backup.Open(r.Context(), s.Store, s.Vault, t)
	if err == nil {
		err = backup.Test(r.Context(), tgt)
		tgt.Close()
	}
	if err != nil {
		FailMsg(w, http.StatusBadGateway, "target_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) listBackups(w http.ResponseWriter, r *http.Request) {
	var dev int64
	if v := r.URL.Query().Get("device"); v != "" {
		r.SetPathValue("id", v)
		dev, _ = PathID(r)
	}
	b, err := s.Store.Backups(r.Context(), dev)
	if err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) downloadBackup(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	b, err := s.Store.Backup(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && b.TargetID == nil) {
		fail(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	t, err := s.Store.BackupTarget(r.Context(), *b.TargetID)
	if err != nil {
		fail(w, http.StatusNotFound, "not_found")
		return
	}
	tgt, err := backup.Open(r.Context(), s.Store, s.Vault, t)
	if err != nil {
		FailMsg(w, http.StatusBadGateway, "target_failed", err.Error())
		return
	}
	defer tgt.Close()
	rc, err := tgt.Open(r.Context(), b.Path)
	if err != nil {
		FailMsg(w, http.StatusBadGateway, "target_failed", err.Error())
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(b.Path)}))
	io.Copy(w, rc)
}

func (s *Server) backupDevice(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadDevice(w, r)
	if !ok {
		return
	}
	in, ok := s.sw.Registry.Get(d.Kind)
	if !ok || !in.Has("backup") {
		fail(w, http.StatusBadRequest, "no_backup_support")
		return
	}
	j, err := s.sw.Software.EnqueueBackup(r.Context(), d, PrincipalFrom(r).User.Username)
	if err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, j)
}

// setConfigKey replaces one top-level key of a device config.
func (s *Server) setConfigKey(w http.ResponseWriter, r *http.Request, d store.Device, key string, v any) {
	cfg := map[string]json.RawMessage{}
	_ = json.Unmarshal(d.Config, &cfg)
	b, _ := json.Marshal(v)
	cfg[key] = b
	d.Config, _ = json.Marshal(cfg)
	if err := s.Store.UpdateDevice(r.Context(), d); err != nil {
		internal(w, err)
		return
	}
	s.dev.Events.Publish("device.changed", map[string]int64{"id": d.ID})
	out, _ := s.Store.Device(r.Context(), d.ID)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) setBackupSettings(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadDevice(w, r)
	if !ok {
		return
	}
	var in jobs.BackupSettings
	if !readJSON(w, r, &in) {
		return
	}
	if in.Keep < 1 || in.Keep > 100 {
		in.Keep = 5
	}
	if in.TargetID != 0 {
		if _, err := s.Store.BackupTarget(r.Context(), in.TargetID); err != nil {
			fail(w, http.StatusBadRequest, "invalid_target")
			return
		}
	}
	s.setConfigKey(w, r, d, "backup", in)
}

func (s *Server) setGuards(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadDevice(w, r)
	if !ok {
		return
	}
	var in []jobs.Guard
	if !readJSON(w, r, &in) {
		return
	}
	for i := range in {
		g := &in[i]
		g.Name = strings.TrimSpace(g.Name)
		if g.Mode != "warn" {
			g.Mode = "block"
		}
		switch g.Type {
		case "ssh":
			if strings.TrimSpace(g.Command) == "" {
				fail(w, http.StatusBadRequest, "guard_incomplete")
				return
			}
		case "http":
			if !strings.HasPrefix(g.URL, "http://") && !strings.HasPrefix(g.URL, "https://") {
				fail(w, http.StatusBadRequest, "guard_incomplete")
				return
			}
		default:
			fail(w, http.StatusBadRequest, "guard_incomplete")
			return
		}
	}
	s.setConfigKey(w, r, d, "guards", in)
}

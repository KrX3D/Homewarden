// Package backup writes backup files to targets: an SMB share or a folder mounted
// into the container.
package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/hirochachacha/go-smb2"

	"github.com/KrX3D/Homewarden/internal/store"
	"github.com/KrX3D/Homewarden/internal/vault"
)

// Config is a target's settings.
type Config struct {
	// smb
	Host         string `json:"host,omitempty"`
	Share        string `json:"share,omitempty"`
	CredentialID int64  `json:"credentialId,omitempty"`
	Domain       string `json:"domain,omitempty"`
	// both: folder inside the share, or the mounted path for local
	Folder string `json:"folder,omitempty"`
}

// Target is an open backup destination.
type Target interface {
	Put(ctx context.Context, rel string, r io.Reader) (int64, error)
	Open(ctx context.Context, rel string) (io.ReadCloser, error)
	Remove(ctx context.Context, rel string) error
	Close() error
}

// Open connects to a target.
func Open(ctx context.Context, st *store.Store, v *vault.Vault, t store.BackupTarget) (Target, error) {
	var c Config
	if err := json.Unmarshal(t.Config, &c); err != nil {
		return nil, err
	}
	switch t.Type {
	case "local":
		if c.Folder == "" || !filepath.IsAbs(c.Folder) {
			return nil, errors.New("local folder must be an absolute path inside the container, e.g. /backups")
		}
		return &local{root: c.Folder}, nil
	case "smb":
		return openSMB(ctx, st, v, c)
	}
	return nil, fmt.Errorf("unknown target type %q", t.Type)
}

// safeRel rejects paths that try to leave the target folder.
func safeRel(rel string) (string, error) {
	clean := path.Clean("/" + strings.ReplaceAll(rel, "\\", "/"))[1:]
	if clean == "" || strings.HasPrefix(clean, "..") {
		return "", errors.New("invalid backup path")
	}
	return clean, nil
}

// ---- local folder

type local struct{ root string }

func (l *local) full(rel string) (string, error) {
	r, err := safeRel(rel)
	if err != nil {
		return "", err
	}
	return filepath.Join(l.root, filepath.FromSlash(r)), nil
}

func (l *local) Put(_ context.Context, rel string, r io.Reader) (int64, error) {
	p, err := l.full(rel)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return 0, err
	}
	tmp := p + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return n, err
	}
	return n, os.Rename(tmp, p)
}

func (l *local) Open(_ context.Context, rel string) (io.ReadCloser, error) {
	p, err := l.full(rel)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

func (l *local) Remove(_ context.Context, rel string) error {
	p, err := l.full(rel)
	if err != nil {
		return err
	}
	return os.Remove(p)
}

func (l *local) Close() error { return nil }

// ---- SMB share

type smbTarget struct {
	sess   *smb2.Session
	share  *smb2.Share
	folder string
}

func openSMB(ctx context.Context, st *store.Store, v *vault.Vault, c Config) (Target, error) {
	if c.Host == "" || c.Share == "" {
		return nil, errors.New("SMB target needs a host and a share")
	}
	user, pass := "", ""
	if c.CredentialID != 0 {
		cred, err := st.Credential(ctx, c.CredentialID)
		if err != nil {
			return nil, fmt.Errorf("credential: %w", err)
		}
		pass, err = v.DecryptString(cred.Secret)
		if err != nil {
			return nil, err
		}
		user = cred.Username
	}
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(c.Host, "445"))
	if err != nil {
		return nil, err
	}
	sd := &smb2.Dialer{Initiator: &smb2.NTLMInitiator{User: user, Password: pass, Domain: c.Domain}}
	sess, err := sd.DialContext(ctx, conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("SMB login: %w", err)
	}
	share, err := sess.Mount(`\\` + c.Host + `\` + c.Share)
	if err != nil {
		sess.Logoff()
		return nil, fmt.Errorf("SMB share %s: %w", c.Share, err)
	}
	return &smbTarget{sess: sess, share: share, folder: strings.Trim(strings.ReplaceAll(c.Folder, "\\", "/"), "/")}, nil
}

func (s *smbTarget) full(rel string) (string, error) {
	r, err := safeRel(rel)
	if err != nil {
		return "", err
	}
	p := r
	if s.folder != "" {
		p = s.folder + "/" + r
	}
	return strings.ReplaceAll(p, "/", `\`), nil
}

func (s *smbTarget) Put(ctx context.Context, rel string, r io.Reader) (int64, error) {
	p, err := s.full(rel)
	if err != nil {
		return 0, err
	}
	fs := s.share.WithContext(ctx)
	if i := strings.LastIndex(p, `\`); i > 0 {
		if err := fs.MkdirAll(p[:i], 0o755); err != nil {
			return 0, err
		}
	}
	tmp := p + ".part"
	f, err := fs.Create(tmp)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		fs.Remove(tmp)
		return n, err
	}
	_ = fs.Remove(p)
	return n, fs.Rename(tmp, p)
}

func (s *smbTarget) Open(ctx context.Context, rel string) (io.ReadCloser, error) {
	p, err := s.full(rel)
	if err != nil {
		return nil, err
	}
	return s.share.WithContext(ctx).Open(p)
}

func (s *smbTarget) Remove(ctx context.Context, rel string) error {
	p, err := s.full(rel)
	if err != nil {
		return err
	}
	return s.share.WithContext(ctx).Remove(p)
}

func (s *smbTarget) Close() error {
	s.share.Umount()
	return s.sess.Logoff()
}

// Test writes, reads back and removes a small file.
func Test(ctx context.Context, t Target) error {
	name := fmt.Sprintf(".homewarden-test-%d", time.Now().UnixNano())
	if _, err := t.Put(ctx, name, strings.NewReader("homewarden")); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	rc, err := t.Open(ctx, name)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "homewarden" {
		return errors.New("read back different content")
	}
	return t.Remove(ctx, name)
}

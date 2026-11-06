// Package conn turns a device's connection settings and stored credentials into a
// working SSH session. Integrations and the onboarding flow both go through here.
package conn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/KrX3D/Homewarden/internal/sshx"
	"github.com/KrX3D/Homewarden/internal/store"
	"github.com/KrX3D/Homewarden/internal/vault"
)

// Settings is the "connection" object inside a device's config.
type Settings struct {
	Method       string `json:"method"` // ssh (agent later)
	Port         int    `json:"port,omitempty"`
	CredentialID int64  `json:"credentialId,omitempty"`
	User         string `json:"user,omitempty"` // overrides the credential's username
	Sudo         bool   `json:"sudo"`           // prefix privileged commands with sudo -n
}

// FromDevice reads the connection settings from a device config.
func FromDevice(d store.Device) Settings {
	var cfg struct {
		Connection Settings `json:"connection"`
	}
	_ = json.Unmarshal(d.Config, &cfg)
	if cfg.Connection.Port == 0 {
		cfg.Connection.Port = 22
	}
	return cfg.Connection
}

// WithSettings returns the device config with the connection object replaced.
func WithSettings(d store.Device, s Settings) (json.RawMessage, error) {
	m := map[string]json.RawMessage{}
	if len(d.Config) > 0 {
		if err := json.Unmarshal(d.Config, &m); err != nil {
			return nil, err
		}
	}
	b, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	m["connection"] = b
	return json.Marshal(m)
}

// Service opens connections.
type Service struct {
	St    *store.Store
	Vault *vault.Vault
}

// hostKeys adapts the store to sshx.HostKeyStore.
type hostKeys struct {
	ctx context.Context
	st  *store.Store
}

func (h hostKeys) Lookup(addr string) (string, string, bool, error) {
	k, err := h.st.KnownHost(h.ctx, addr)
	if errors.Is(err, store.ErrNotFound) {
		return "", "", false, nil
	}
	return k.KeyType, k.Key, err == nil, err
}

func (h hostKeys) Pin(addr, typ, fp, key string) error {
	return h.st.PinHost(h.ctx, store.KnownHost{Address: addr, KeyType: typ, Fingerprint: fp, Key: key})
}

// ErrNotConfigured means the device has no usable connection yet.
var ErrNotConfigured = errors.New("connection not configured")

// Target builds the SSH target for a device, decrypting its credential.
func (s *Service) Target(ctx context.Context, d store.Device) (sshx.Target, Settings, error) {
	set := FromDevice(d)
	if set.Method != "ssh" || set.CredentialID == 0 || d.Host == "" {
		return sshx.Target{}, set, ErrNotConfigured
	}
	c, err := s.St.Credential(ctx, set.CredentialID)
	if err != nil {
		return sshx.Target{}, set, fmt.Errorf("credential: %w", err)
	}
	secret, err := s.Vault.DecryptString(c.Secret)
	if err != nil {
		return sshx.Target{}, set, err
	}
	t := sshx.Target{Host: d.Host, Port: set.Port, User: c.Username, HostKeys: hostKeys{ctx, s.St}}
	if set.User != "" {
		t.User = set.User
	}
	switch c.Type {
	case store.CredSSHKey:
		t.KeyPEM = secret
	case store.CredPassword:
		t.Password = secret
	default:
		return t, set, errors.New("this credential type cannot be used for SSH")
	}
	return t, set, nil
}

// HostKeysFor exposes host-key pinning for one-off connections (key install).
func (s *Service) HostKeysFor(ctx context.Context) sshx.HostKeyStore { return hostKeys{ctx, s.St} }

// Dial connects to a device.
func (s *Service) Dial(ctx context.Context, d store.Device) (*ssh.Client, Settings, error) {
	t, set, err := s.Target(ctx, d)
	if err != nil {
		return nil, set, err
	}
	c, err := sshx.Dial(ctx, t)
	return c, set, err
}

// Exec runs one command on a device. With privileged=true and sudo enabled in the
// device settings, the command runs through `sudo -n` (never prompts).
func (s *Service) Exec(ctx context.Context, d store.Device, cmd string, privileged bool, timeout time.Duration) (sshx.Result, error) {
	c, set, err := s.Dial(ctx, d)
	if err != nil {
		return sshx.Result{}, err
	}
	defer c.Close()
	if privileged && set.Sudo {
		cmd = "sudo -n sh -c " + sshx.ShellQuote(cmd)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return sshx.Run(ctx, c, cmd, "")
}

// Facts is what a connection test reports.
type Facts struct {
	User         string `json:"user"`
	Hostname     string `json:"hostname"`
	OS           string `json:"os"`
	Kernel       string `json:"kernel"`
	Arch         string `json:"arch"`
	Sudo         bool   `json:"sudo"`
	HostKey      string `json:"hostKey"`
	HostKeyFirst bool   `json:"hostKeyFirst"`
}

// factsScript prints key=value lines; it only uses POSIX sh and coreutils.
const factsScript = `echo "user=$(id -un)"
echo "hostname=$(hostname)"
. /etc/os-release 2>/dev/null && echo "os=$PRETTY_NAME"
echo "kernel=$(uname -r)"
echo "arch=$(uname -m)"
if sudo -n true 2>/dev/null; then echo "sudo=yes"; else echo "sudo=no"; fi`

// Test connects to a device and reports what it found.
func (s *Service) Test(ctx context.Context, d store.Device) (Facts, error) {
	t, _, err := s.Target(ctx, d)
	if err != nil {
		return Facts{}, err
	}
	_, pinErr := s.St.KnownHost(ctx, t.Addr())
	c, err := sshx.Dial(ctx, t)
	if err != nil {
		return Facts{}, err
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	res, err := sshx.Run(ctx, c, factsScript, "")
	if err != nil {
		return Facts{}, err
	}
	f := ParseFacts(res.Stdout)
	f.HostKeyFirst = errors.Is(pinErr, store.ErrNotFound)
	if k, err := s.St.KnownHost(ctx, t.Addr()); err == nil {
		f.HostKey = k.Fingerprint
	}
	return f, nil
}

// ParseFacts reads the key=value output of factsScript.
func ParseFacts(out string) Facts {
	var f Facts
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "user":
			f.User = v
		case "hostname":
			f.Hostname = v
		case "os":
			f.OS = v
		case "kernel":
			f.Kernel = v
		case "arch":
			f.Arch = v
		case "sudo":
			f.Sudo = v == "yes"
		}
	}
	return f
}

// InstallKey logs in once with a password (never stored), appends the credential's
// public key to ~/.ssh/authorized_keys if it is not there yet, and checks that key
// login then works.
func (s *Service) InstallKey(ctx context.Context, d store.Device, port int, user, password string, cred store.Credential) error {
	if cred.Type != store.CredSSHKey || cred.Public == "" {
		return errors.New("pick an SSH key credential")
	}
	if port == 0 {
		port = 22
	}
	t := sshx.Target{Host: d.Host, Port: port, User: user, Password: password, HostKeys: s.HostKeysFor(ctx)}
	c, err := sshx.Dial(ctx, t)
	if err != nil {
		return err
	}
	defer c.Close()
	script := `set -e
umask 077
mkdir -p "$HOME/.ssh"
touch "$HOME/.ssh/authorized_keys"
key=$(cat)
grep -qxF "$key" "$HOME/.ssh/authorized_keys" || printf '%s\n' "$key" >> "$HOME/.ssh/authorized_keys"
chmod 700 "$HOME/.ssh"; chmod 600 "$HOME/.ssh/authorized_keys"`
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	res, err := sshx.Run(rctx, c, "sh -c "+sshx.ShellQuote(script), cred.Public+"\n")
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("installing the key failed: %s", strings.TrimSpace(res.Stderr))
	}
	priv, err := s.Vault.DecryptString(cred.Secret)
	if err != nil {
		return err
	}
	kc, err := sshx.Dial(ctx, sshx.Target{Host: d.Host, Port: port, User: user, KeyPEM: priv, HostKeys: s.HostKeysFor(ctx)})
	if err != nil {
		return fmt.Errorf("key installed, but logging in with it failed: %w", err)
	}
	kc.Close()
	return nil
}

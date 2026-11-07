package conn

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/KrX3D/Homewarden/internal/sshx"
	"github.com/KrX3D/Homewarden/internal/store"
)

// Login says how to reach a device once for a setup step: with a user and password
// typed in for this step only (never stored), or with the device's saved connection.
type Login struct {
	User     string
	Password string
	Port     int
}

// RunScriptAsRoot copies script to the device over SSH and runs it as root with args:
// directly when the login is root, else through sudo (with the password when one was
// given, sudo -n otherwise). It returns the combined output.
func (s *Service) RunScriptAsRoot(ctx context.Context, d store.Device, l Login, script string, args []string, timeout time.Duration) (string, error) {
	var c *ssh.Client
	var err error
	user := l.User
	if l.Password != "" {
		port := l.Port
		if port == 0 {
			port = 22
		}
		if d.Host == "" || user == "" {
			return "", errors.New("address and user are needed")
		}
		c, err = sshx.Dial(ctx, sshx.Target{Host: d.Host, Port: port, User: user, Password: l.Password, HostKeys: s.HostKeysFor(ctx)})
	} else {
		c, _, err = s.Dial(ctx, d)
	}
	if err != nil {
		return "", err
	}
	defer c.Close()
	user = c.User()

	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	const path = "/tmp/homewarden-setup.sh"
	if res, err := sshx.Run(rctx, c, "umask 077; cat > "+path, script); err != nil || res.ExitCode != 0 {
		if err == nil {
			err = errors.New(strings.TrimSpace(res.Stderr))
		}
		return "", fmt.Errorf("copying the installer failed: %w", err)
	}
	defer func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		_, _ = sshx.Run(cctx, c, "rm -f "+path, "")
	}()

	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = sshx.ShellQuote(a)
	}
	cmd := "sh " + path + " " + strings.Join(quoted, " ")
	stdin := ""
	switch {
	case user == "root":
	case l.Password != "":
		cmd = "sudo -S -p '' " + cmd
		stdin = l.Password + "\n"
	default:
		cmd = "sudo -n " + cmd
	}
	res, err := sshx.Run(rctx, c, cmd, stdin)
	out := strings.TrimSpace(res.Stdout + "\n" + res.Stderr)
	if err != nil {
		return out, err
	}
	if res.ExitCode != 0 {
		if user != "root" && strings.Contains(out, "sudo") {
			return out, fmt.Errorf("%s cannot run the installer as root through sudo. Log in as root, or enter a user and password with full sudo rights", user)
		}
		return out, fmt.Errorf("installer exited with %d", res.ExitCode)
	}
	return out, nil
}

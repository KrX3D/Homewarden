// Package sshx wraps golang.org/x/crypto/ssh: key generation, host-key pinning and
// running commands with a timeout.
package sshx

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// GenerateKey returns a new ed25519 key as an OpenSSH private key (PEM) and an
// authorized_keys line with the given comment.
func GenerateKey(comment string) (privPEM, authorized string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return "", "", err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", "", err
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
	if comment != "" {
		line += " " + comment
	}
	return string(pem.EncodeToMemory(block)), line, nil
}

// PublicFromPrivate derives the authorized_keys line from a pasted private key.
func PublicFromPrivate(privPEM, comment string) (string, error) {
	signer, err := ssh.ParsePrivateKey([]byte(privPEM))
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	if comment != "" {
		line += " " + comment
	}
	return line, nil
}

// Fingerprint is the SHA256 fingerprint as ssh-keygen prints it.
func Fingerprint(k ssh.PublicKey) string { return ssh.FingerprintSHA256(k) }

// HostKeyStore pins host keys on first use.
type HostKeyStore interface {
	// Lookup returns the pinned key (base64 wire format) or found=false.
	Lookup(addr string) (keyType, keyB64 string, found bool, err error)
	// Pin stores the key seen on first contact.
	Pin(addr, keyType, fingerprint, keyB64 string) error
}

// HostKeyMismatch means the server presented a different key than the pinned one:
// either the device was reinstalled or something is intercepting the connection.
type HostKeyMismatch struct {
	Address  string
	Expected string
	Got      string
}

func (e *HostKeyMismatch) Error() string {
	return fmt.Sprintf("host key of %s changed: pinned %s, got %s", e.Address, e.Expected, e.Got)
}

// Callback implements trust-on-first-use against a store.
func Callback(st HostKeyStore) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		addr := hostname
		keyB64 := base64.StdEncoding.EncodeToString(key.Marshal())
		typ, pinned, found, err := st.Lookup(addr)
		if err != nil {
			return err
		}
		if !found {
			return st.Pin(addr, key.Type(), Fingerprint(key), keyB64)
		}
		if typ == key.Type() && pinned == keyB64 {
			return nil
		}
		expected := "?"
		if raw, err := base64.StdEncoding.DecodeString(pinned); err == nil {
			if k, err := ssh.ParsePublicKey(raw); err == nil {
				expected = Fingerprint(k)
			}
		}
		return &HostKeyMismatch{Address: addr, Expected: expected, Got: Fingerprint(key)}
	}
}

// Target is where and how to connect.
type Target struct {
	Host     string
	Port     int
	User     string
	Password string // used when set
	KeyPEM   string // used when set
	HostKeys HostKeyStore
	Timeout  time.Duration
}

// Addr is host:port.
func (t Target) Addr() string {
	p := t.Port
	if p == 0 {
		p = 22
	}
	return net.JoinHostPort(t.Host, strconv.Itoa(p))
}

// Dial opens an SSH connection.
func Dial(ctx context.Context, t Target) (*ssh.Client, error) {
	if t.User == "" {
		return nil, errors.New("no SSH user set")
	}
	auth := []ssh.AuthMethod{}
	if t.KeyPEM != "" {
		signer, err := ssh.ParsePrivateKey([]byte(t.KeyPEM))
		if err != nil {
			return nil, fmt.Errorf("private key: %w", err)
		}
		auth = append(auth, ssh.PublicKeys(signer))
	}
	if t.Password != "" {
		pw := t.Password
		auth = append(auth, ssh.Password(pw), ssh.KeyboardInteractive(func(_, _ string, qs []string, _ []bool) ([]string, error) {
			ans := make([]string, len(qs))
			for i := range ans {
				ans[i] = pw
			}
			return ans, nil
		}))
	}
	if len(auth) == 0 {
		return nil, errors.New("no SSH key or password configured")
	}
	if t.Timeout == 0 {
		t.Timeout = 10 * time.Second
	}
	cb := ssh.InsecureIgnoreHostKey()
	if t.HostKeys != nil {
		cb = Callback(t.HostKeys)
	}
	cfg := &ssh.ClientConfig{User: t.User, Auth: auth, HostKeyCallback: cb, Timeout: t.Timeout}
	d := net.Dialer{Timeout: t.Timeout}
	conn, err := d.DialContext(ctx, "tcp", t.Addr())
	if err != nil {
		return nil, err
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, t.Addr(), cfg)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return ssh.NewClient(c, chans, reqs), nil
}

// Result of a command.
type Result struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
}

// Run executes cmd and waits for it, honouring ctx. A non-zero exit is not an error;
// check ExitCode.
func Run(ctx context.Context, c *ssh.Client, cmd string, stdin string) (Result, error) {
	sess, err := c.NewSession()
	if err != nil {
		return Result{}, err
	}
	defer sess.Close()
	var out, errb bytes.Buffer
	sess.Stdout = &out
	sess.Stderr = &errb
	if stdin != "" {
		sess.Stdin = strings.NewReader(stdin)
	}
	done := make(chan error, 1)
	go func() { done <- sess.Run(cmd) }()
	select {
	case <-ctx.Done():
		sess.Signal(ssh.SIGKILL)
		sess.Close()
		return Result{Stdout: out.String(), Stderr: errb.String(), ExitCode: -1}, ctx.Err()
	case err := <-done:
		res := Result{Stdout: out.String(), Stderr: errb.String()}
		var exit *ssh.ExitError
		if errors.As(err, &exit) {
			res.ExitCode = exit.ExitStatus()
			return res, nil
		}
		if err != nil {
			res.ExitCode = -1
			return res, err
		}
		return res, nil
	}
}

// ShellQuote quotes s for a POSIX shell.
func ShellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'" }

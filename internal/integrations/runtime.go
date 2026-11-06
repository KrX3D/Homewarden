package integrations

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
	"golang.org/x/crypto/ssh"

	"github.com/KrX3D/Homewarden/internal/conn"
	"github.com/KrX3D/Homewarden/internal/sshx"
	"github.com/KrX3D/Homewarden/internal/store"
	"github.com/KrX3D/Homewarden/internal/vault"
)

// Host provides the services the sandbox may hand to scripts.
type Host struct {
	St    *store.Store
	Vault *vault.Vault
	Conn  *conn.Service
}

// Settings returns the integration settings stored in a device's config.
func Settings(d store.Device) map[string]any {
	var cfg struct {
		Settings map[string]any `json:"settings"`
	}
	_ = json.Unmarshal(d.Config, &cfg)
	if cfg.Settings == nil {
		cfg.Settings = map[string]any{}
	}
	return cfg.Settings
}

// ScriptError is an error thrown by integration code (as opposed to a Go failure).
type ScriptError struct{ Msg string }

func (e *ScriptError) Error() string { return e.Msg }

// call is the state of one script invocation.
type call struct {
	ctx    context.Context
	host   *Host
	in     *Integration
	dev    store.Device
	logf   func(string)
	vm     *goja.Runtime
	sshMu  sync.Mutex
	client *ssh.Client
	set    conn.Settings
}

// Call runs fn(ctx, arg) of an integration against a device and returns the result
// as JSON. The script is interrupted when ctx ends.
func (h *Host) Call(ctx context.Context, in *Integration, dev store.Device, fn string, arg any, logf func(string)) (json.RawMessage, error) {
	if logf == nil {
		logf = func(string) {}
	}
	vm := goja.New()
	vm.SetFieldNameMapper(goja.TagFieldNameMapper("json", true))
	c := &call{ctx: ctx, host: h, in: in, dev: dev, logf: logf, vm: vm}
	defer func() {
		if c.client != nil {
			c.client.Close()
		}
	}()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			vm.Interrupt("cancelled: " + ctx.Err().Error())
		case <-stop:
		}
	}()

	if _, err := vm.RunProgram(in.prog); err != nil {
		return nil, scriptErr(err)
	}
	f, ok := goja.AssertFunction(vm.Get(fn))
	if !ok {
		return nil, fmt.Errorf("%s has no function %s", in.ID, fn)
	}
	res, err := f(goja.Undefined(), c.ctxObject(), vm.ToValue(arg))
	if err != nil {
		return nil, scriptErr(err)
	}
	if res == nil || goja.IsUndefined(res) || goja.IsNull(res) {
		return json.RawMessage("null"), nil
	}
	b, err := json.Marshal(res.Export())
	if err != nil {
		return nil, fmt.Errorf("%s.%s returned something that is not JSON: %w", in.ID, fn, err)
	}
	return b, nil
}

func scriptErr(err error) error {
	var ex *goja.Exception
	if errors.As(err, &ex) {
		return &ScriptError{Msg: ex.Value().String()}
	}
	var intr *goja.InterruptedError
	if errors.As(err, &intr) {
		return &ScriptError{Msg: fmt.Sprint(intr.Value())}
	}
	return err
}

// throw raises a JS exception from Go.
func (c *call) throw(format string, args ...any) {
	panic(c.vm.NewGoError(fmt.Errorf(format, args...)))
}

func (c *call) ctxObject() *goja.Object {
	vm := c.vm
	o := vm.NewObject()
	o.Set("device", map[string]any{"id": c.dev.ID, "name": c.dev.Name, "host": c.dev.Host, "kind": c.dev.Kind})
	o.Set("settings", Settings(c.dev))
	o.Set("log", func(call goja.FunctionCall) goja.Value {
		parts := make([]string, len(call.Arguments))
		for i, a := range call.Arguments {
			if a.ExportType() != nil && a.ExportType().Kind().String() == "map" {
				b, _ := json.Marshal(a.Export())
				parts[i] = string(b)
			} else {
				parts[i] = a.String()
			}
		}
		c.logf(strings.Join(parts, " "))
		return goja.Undefined()
	})
	o.Set("sleep", func(ms int64) {
		if ms > 60000 {
			ms = 60000
		}
		select {
		case <-time.After(time.Duration(ms) * time.Millisecond):
		case <-c.ctx.Done():
		}
	})
	o.Set("now", func() int64 { return time.Now().Unix() })
	// Lets integrations recognise Homewarden's own container (Docker sets the
	// hostname to the short container id).
	hn, _ := os.Hostname()
	o.Set("self", map[string]any{"hostname": hn})
	o.Set("secret", c.secret)

	util := vm.NewObject()
	util.Set("compareVersions", CompareVersions)
	o.Set("util", util)

	sshObj := vm.NewObject()
	sshObj.Set("exec", c.sshExec)
	o.Set("ssh", sshObj)

	httpObj := vm.NewObject()
	httpObj.Set("request", c.httpRequest)
	o.Set("http", httpObj)

	dockerObj := vm.NewObject()
	dockerObj.Set("request", c.dockerRequest)
	o.Set("docker", dockerObj)
	return o
}

// secret returns the decrypted credential chosen in a credential setting.
func (c *call) secret(key string) map[string]any {
	var def *Setting
	for i := range c.in.Settings {
		if c.in.Settings[i].Key == key {
			def = &c.in.Settings[i]
		}
	}
	if def == nil || def.Type != "credential" {
		c.throw("secret(%q): not a credential setting of this integration", key)
	}
	raw, ok := Settings(c.dev)[key]
	if !ok || raw == nil {
		return nil
	}
	var id int64
	switch v := raw.(type) {
	case float64:
		id = int64(v)
	case string:
		fmt.Sscan(v, &id)
	}
	if id == 0 {
		return nil
	}
	cred, err := c.host.St.Credential(c.ctx, id)
	if err != nil {
		c.throw("credential %d: %v", id, err)
	}
	sec, err := c.host.Vault.DecryptString(cred.Secret)
	if err != nil {
		c.throw("credential %d: %v", id, err)
	}
	return map[string]any{"username": cred.Username, "secret": sec, "type": cred.Type}
}

type execOpts struct {
	Sudo    bool   `json:"sudo"`
	Timeout int    `json:"timeout"` // seconds
	Stdin   string `json:"stdin"`
}

func (c *call) sshExec(cmd string, opts *execOpts) map[string]any {
	if !c.in.Permissions.SSH {
		c.throw("this integration has no SSH permission")
	}
	if opts == nil {
		opts = &execOpts{}
	}
	c.sshMu.Lock()
	if c.client == nil {
		cl, set, err := c.host.Conn.Dial(c.ctx, c.dev)
		if err != nil {
			c.sshMu.Unlock()
			if errors.Is(err, conn.ErrNotConfigured) {
				c.throw("SSH is not set up for this device: pick a credential in the Connection card")
			}
			c.throw("SSH: %v", err)
		}
		c.client, c.set = cl, set
	}
	client, set := c.client, c.set
	c.sshMu.Unlock()
	if opts.Sudo && client.User() != "root" {
		if !set.Sudo {
			c.throw("this needs root: log in as root, or turn on \"Use sudo for updates\" in the Connection card")
		}
		// Prefix rather than wrap in sh -c: a sudoers rule that only allows the package
		// manager must be enough. Scripts pass one command, env vars in front
		// (needs SETENV in the sudoers rule).
		cmd = "sudo -n " + cmd
	}
	timeout := time.Duration(opts.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(c.ctx, timeout)
	defer cancel()
	res, err := sshx.Run(ctx, client, cmd, opts.Stdin)
	if err != nil && res.ExitCode == -1 {
		c.throw("SSH command failed: %v", err)
	}
	return map[string]any{"stdout": res.Stdout, "stderr": res.Stderr, "code": res.ExitCode}
}

type httpOpts struct {
	Method   string            `json:"method"`
	URL      string            `json:"url"`
	Headers  map[string]string `json:"headers"`
	Body     any               `json:"body"`
	Timeout  int               `json:"timeout"`
	Insecure bool              `json:"insecure"`
}

// allowedHost checks a URL host against the manifest permissions.
func (c *call) allowedHost(host string) bool { return AllowedHost(c.in, c.dev, host) }

// AllowedHost reports whether an integration may reach host for this device. The core
// uses it too, e.g. before downloading a backup URL an integration returned.
func AllowedHost(in *Integration, dev store.Device, host string) bool {
	h := strings.ToLower(host)
	if in.Permissions.HTTPDevice && strings.EqualFold(h, dev.Host) {
		return true
	}
	hosts := in.Permissions.HTTPHosts
	if key := in.Permissions.HTTPHostsSetting; key != "" {
		if v, ok := Settings(dev)[key].(string); ok {
			for _, h := range strings.Split(v, ",") {
				if h = strings.TrimSpace(h); h != "" {
					hosts = append(hosts, h)
				}
			}
		}
	}
	for _, p := range hosts {
		p = strings.ToLower(p)
		if p == h || (strings.HasPrefix(p, "*.") && strings.HasSuffix(h, p[1:])) {
			return true
		}
	}
	return false
}

func (c *call) httpRequest(o httpOpts) map[string]any {
	u, err := url.Parse(o.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		c.throw("http.request: invalid URL %q", o.URL)
	}
	if !c.allowedHost(u.Hostname()) {
		c.throw("http.request: %s is not in this integration's allowed hosts", u.Hostname())
	}
	if o.Method == "" {
		o.Method = "GET"
	}
	var body io.Reader
	isJSON := false
	switch b := o.Body.(type) {
	case nil:
	case string:
		body = strings.NewReader(b)
	default:
		j, _ := json.Marshal(b)
		body = bytes.NewReader(j)
		isJSON = true
	}
	timeout := time.Duration(o.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(c.ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, o.Method, u.String(), body)
	if err != nil {
		c.throw("http.request: %v", err)
	}
	for k, v := range o.Headers {
		req.Header.Set(k, v)
	}
	if isJSON && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: o.Insecure}, Proxy: http.ProxyFromEnvironment},
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) > 5 || !c.allowedHost(r.URL.Hostname()) {
				return errors.New("redirect to a host that is not allowed")
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		c.throw("http.request %s: %v", u.Host, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	headers := map[string]string{}
	for k := range resp.Header {
		headers[strings.ToLower(k)] = resp.Header.Get(k)
	}
	out := map[string]any{"status": resp.StatusCode, "headers": headers, "body": string(raw)}
	var j any
	if json.Unmarshal(raw, &j) == nil {
		out["json"] = j
	}
	return out
}

// dockerRequest talks to a Docker Engine API: the local socket or tcp://host:port
// from the device's dockerEndpoint setting.
func (c *call) dockerRequest(method, path string, body any) map[string]any {
	if !c.in.Permissions.Docker {
		c.throw("this integration has no Docker permission")
	}
	ep, _ := Settings(c.dev)["dockerEndpoint"].(string)
	if ep == "" {
		ep = "unix:///var/run/docker.sock"
	}
	var transport http.RoundTripper
	base := ""
	switch {
	case strings.HasPrefix(ep, "unix://"):
		sock := strings.TrimPrefix(ep, "unix://")
		transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		}}
		base = "http://docker"
	case strings.HasPrefix(ep, "tcp://"):
		transport = http.DefaultTransport
		base = "http://" + strings.TrimPrefix(ep, "tcp://")
	default:
		c.throw("dockerEndpoint must start with unix:// or tcp://")
	}
	if !strings.HasPrefix(path, "/") {
		c.throw("docker.request: path must start with /")
	}
	var rb io.Reader
	if body != nil {
		j, _ := json.Marshal(body)
		rb = bytes.NewReader(j)
	}
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, base+path, rb)
	if err != nil {
		c.throw("docker.request: %v", err)
	}
	if rb != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		c.throw("Docker API: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	out := map[string]any{"status": resp.StatusCode, "body": string(raw)}
	var j any
	if json.Unmarshal(raw, &j) == nil {
		out["json"] = j
	}
	return out
}

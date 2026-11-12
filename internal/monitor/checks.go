// Package monitor runs uptime checks: HTTP (status, keyword, JSON value, certificate
// expiry), TCP, ping, DNS, Docker container state and push heartbeats.
package monitor

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/KrX3D/Homewarden/internal/integrations"
	"github.com/KrX3D/Homewarden/internal/presence"
)

// Config holds every type's options; unused ones stay empty.
type Config struct {
	URL          string `json:"url,omitempty"`
	Method       string `json:"method,omitempty"`
	ExpectStatus string `json:"expectStatus,omitempty"` // e.g. "200-399" or "200,204"
	Keyword      string `json:"keyword,omitempty"`
	InvertKey    bool   `json:"invertKeyword,omitempty"`
	JSONPath     string `json:"jsonPath,omitempty"` // dotted path, e.g. status.health
	JSONExpect   string `json:"jsonExpect,omitempty"`
	Insecure     bool   `json:"insecure,omitempty"`
	CertDays     int    `json:"certDays,omitempty"` // warn when the certificate expires sooner
	Host         string `json:"host,omitempty"`
	Port         int    `json:"port,omitempty"`
	Record       string `json:"record,omitempty"` // A | AAAA | CNAME | MX | TXT
	Server       string `json:"server,omitempty"` // DNS server, empty = system
	Expect       string `json:"expect,omitempty"` // DNS: answer must contain
	Timeout      int    `json:"timeout,omitempty"`

	// Container: name or id, and the Docker endpoint (empty = the Docker setup of the
	// monitor's device or its host, else the local socket).
	Container string `json:"container,omitempty"`
	Endpoint  string `json:"endpoint,omitempty"`

	// Alerting, all types.
	Retries       int    `json:"retries,omitempty"`       // failed checks in a row before down (0 = 2)
	RetryInterval int    `json:"retryInterval,omitempty"` // seconds between checks while failing (0 = interval)
	ResendEvery   int    `json:"resendEvery,omitempty"`   // repeat the down alert every N failed checks (0 = once)
	UpsideDown    bool   `json:"upsideDown,omitempty"`    // up when the check fails (something must stay unreachable)
	CertWarn      string `json:"certWarn,omitempty"`      // notify this many days before the certificate expires, e.g. "14,7"

	// Area on the overview (servers, network, storage, apps); empty = from the device.
	Area string `json:"area,omitempty"`
}

// FailuresForDown is how many failed checks in a row mark the monitor down.
func (c Config) FailuresForDown() int {
	if c.Retries <= 0 {
		return 2
	}
	return c.Retries
}

// CertWarnDays parses CertWarn into days, largest first.
func (c Config) CertWarnDays() []int {
	out := []int{}
	for _, part := range strings.Split(c.CertWarn, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(part)); err == nil && n > 0 {
			out = append(out, n)
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(out)))
	return out
}

// Result of one check.
type Result struct {
	OK      bool
	Ms      float64
	Msg     string
	CertEnd *int64
}

func timeout(c Config) time.Duration {
	if c.Timeout <= 0 {
		return 10 * time.Second
	}
	return time.Duration(c.Timeout) * time.Second
}

// statusOK checks a status code against "200-399" / "200,204" style rules.
func statusOK(rule string, code int) bool {
	if rule == "" {
		rule = "200-399"
	}
	for _, part := range strings.Split(rule, ",") {
		part = strings.TrimSpace(part)
		if lo, hi, ok := strings.Cut(part, "-"); ok {
			l, _ := strconv.Atoi(lo)
			h, _ := strconv.Atoi(hi)
			if code >= l && code <= h {
				return true
			}
		} else if n, err := strconv.Atoi(part); err == nil && n == code {
			return true
		}
	}
	return false
}

// jsonValue walks a dotted path ("a.b.0.c") through decoded JSON.
func jsonValue(v any, path string) (string, bool) {
	for _, key := range strings.Split(path, ".") {
		switch x := v.(type) {
		case map[string]any:
			v = x[key]
		case []any:
			i, err := strconv.Atoi(key)
			if err != nil || i < 0 || i >= len(x) {
				return "", false
			}
			v = x[i]
		default:
			return "", false
		}
	}
	if v == nil {
		return "", false
	}
	return fmt.Sprint(v), true
}

// Check runs one check of the given type.
func Check(ctx context.Context, typ string, c Config) Result {
	ctx, cancel := context.WithTimeout(ctx, timeout(c))
	defer cancel()
	start := time.Now()
	ms := func() float64 { return float64(time.Since(start).Microseconds()) / 1000 }
	switch typ {
	case "http":
		method := c.Method
		if method == "" {
			method = http.MethodGet
		}
		req, err := http.NewRequestWithContext(ctx, method, c.URL, nil)
		if err != nil {
			return Result{Msg: err.Error()}
		}
		req.Header.Set("User-Agent", "Homewarden-Monitor")
		client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: c.Insecure}}}
		resp, err := client.Do(req)
		if err != nil {
			return Result{Ms: ms(), Msg: err.Error()}
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		r := Result{Ms: ms()}
		if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
			end := resp.TLS.PeerCertificates[0].NotAfter.Unix()
			r.CertEnd = &end
		}
		if !statusOK(c.ExpectStatus, resp.StatusCode) {
			r.Msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
			return r
		}
		if c.Keyword != "" {
			found := strings.Contains(string(body), c.Keyword)
			if found == c.InvertKey {
				if c.InvertKey {
					r.Msg = "keyword found: " + c.Keyword
				} else {
					r.Msg = "keyword missing: " + c.Keyword
				}
				return r
			}
		}
		if c.JSONPath != "" {
			var v any
			if err := json.Unmarshal(body, &v); err != nil {
				r.Msg = "not JSON"
				return r
			}
			got, ok := jsonValue(v, c.JSONPath)
			if !ok || (c.JSONExpect != "" && got != c.JSONExpect) {
				r.Msg = fmt.Sprintf("%s = %q", c.JSONPath, got)
				return r
			}
		}
		r.OK, r.Msg = true, fmt.Sprintf("HTTP %d", resp.StatusCode)
		if r.CertEnd != nil && c.CertDays > 0 && time.Until(time.Unix(*r.CertEnd, 0)) < time.Duration(c.CertDays)*24*time.Hour {
			r.OK = false
			r.Msg = fmt.Sprintf("certificate expires %s", time.Unix(*r.CertEnd, 0).Format("2006-01-02"))
		}
		return r
	case "tcp":
		d, err := presence.TCP(ctx, c.Host, c.Port, timeout(c))
		if err != nil {
			return Result{Msg: err.Error()}
		}
		return Result{OK: true, Ms: float64(d.Microseconds()) / 1000, Msg: "open"}
	case "ping":
		d, err := presence.Ping(ctx, c.Host, timeout(c))
		if err != nil {
			return Result{Msg: err.Error()}
		}
		return Result{OK: true, Ms: float64(d.Microseconds()) / 1000, Msg: "reply"}
	case "dns":
		res := net.DefaultResolver
		if c.Server != "" {
			server := c.Server
			if !strings.Contains(server, ":") {
				server += ":53"
			}
			res = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, network, server)
			}}
		}
		answers, err := lookup(ctx, res, c.Record, c.Host)
		r := Result{Ms: ms()}
		if err != nil {
			r.Msg = err.Error()
			return r
		}
		joined := strings.Join(answers, ", ")
		if c.Expect != "" && !strings.Contains(joined, c.Expect) {
			r.Msg = "answer: " + joined
			return r
		}
		r.OK, r.Msg = true, joined
		return r
	case "container":
		r := checkContainer(ctx, c)
		r.Ms = ms()
		return r
	}
	return Result{Msg: "unknown type " + typ}
}

// checkContainer reads a container's state: running and healthy (or without health
// check) is up, still starting counts as up, unhealthy or not running is down.
func checkContainer(ctx context.Context, c Config) Result {
	transport, base, err := integrations.DockerTransport(c.Endpoint)
	if err != nil {
		return Result{Msg: err.Error()}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/containers/"+url.PathEscape(strings.TrimPrefix(c.Container, "/"))+"/json", nil)
	if err != nil {
		return Result{Msg: err.Error()}
	}
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		return Result{Msg: "Docker API: " + err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Result{Msg: "container not found: " + c.Container}
	}
	if resp.StatusCode != http.StatusOK {
		return Result{Msg: fmt.Sprintf("Docker API: HTTP %d", resp.StatusCode)}
	}
	var info struct {
		State struct {
			Status   string
			ExitCode int
			Health   *struct{ Status string }
		}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&info); err != nil {
		return Result{Msg: "Docker API: " + err.Error()}
	}
	return containerResult(info.State.Status, info.State.ExitCode, info.State.Health)
}

func containerResult(status string, exit int, health *struct{ Status string }) Result {
	if status != "running" {
		if status == "exited" {
			return Result{Msg: fmt.Sprintf("exited (code %d)", exit)}
		}
		return Result{Msg: status}
	}
	if health == nil || health.Status == "" || health.Status == "none" {
		return Result{OK: true, Msg: "running"}
	}
	if health.Status == "unhealthy" {
		return Result{Msg: "running, unhealthy"}
	}
	return Result{OK: true, Msg: "running, " + health.Status}
}

func lookup(ctx context.Context, r *net.Resolver, record, name string) ([]string, error) {
	switch strings.ToUpper(record) {
	case "", "A", "AAAA":
		ips, err := r.LookupIPAddr(ctx, name)
		if err != nil {
			return nil, err
		}
		out := []string{}
		for _, ip := range ips {
			v4 := ip.IP.To4() != nil
			if strings.ToUpper(record) == "AAAA" && v4 || strings.ToUpper(record) != "AAAA" && !v4 {
				continue
			}
			out = append(out, ip.IP.String())
		}
		if len(out) == 0 {
			return nil, errors.New("no " + record + " record")
		}
		return out, nil
	case "CNAME":
		c, err := r.LookupCNAME(ctx, name)
		return []string{c}, err
	case "MX":
		mx, err := r.LookupMX(ctx, name)
		out := []string{}
		for _, m := range mx {
			out = append(out, m.Host)
		}
		return out, err
	case "TXT":
		return r.LookupTXT(ctx, name)
	}
	return nil, errors.New("unsupported record type " + record)
}

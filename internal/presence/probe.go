package presence

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"

	"github.com/KrX3D/Homewarden/internal/store"
)

var pingSeq atomic.Uint32

// Ping sends one ICMP echo and waits for the reply.
//
// It uses an unprivileged ICMP socket ("udp4"), which Docker allows by default
// (net.ipv4.ping_group_range), so Homewarden does not need root or NET_RAW. When that
// is refused it falls back to a raw socket, which works when running as root.
func Ping(ctx context.Context, host string, timeout time.Duration) (time.Duration, error) {
	ip, err := resolve4(ctx, host)
	if err != nil {
		return 0, err
	}
	network, laddr := "udp4", "0.0.0.0"
	conn, err := icmp.ListenPacket(network, laddr)
	if err != nil {
		network = "ip4:icmp"
		conn, err = icmp.ListenPacket(network, laddr)
		if err != nil {
			return 0, fmt.Errorf("ping not permitted: %w", err)
		}
	}
	defer conn.Close()

	seq := int(pingSeq.Add(1) & 0xffff)
	payload := []byte("homewarden-" + strconv.Itoa(seq))
	msg := icmp.Message{Type: ipv4.ICMPTypeEcho, Body: &icmp.Echo{ID: os.Getpid() & 0xffff, Seq: seq, Data: payload}}
	wb, err := msg.Marshal(nil)
	if err != nil {
		return 0, err
	}
	var dst net.Addr = &net.IPAddr{IP: ip}
	if network == "udp4" {
		dst = &net.UDPAddr{IP: ip}
	}
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	conn.SetDeadline(deadline)
	start := time.Now()
	if _, err := conn.WriteTo(wb, dst); err != nil {
		return 0, err
	}
	rb := make([]byte, 1500)
	for {
		n, peer, err := conn.ReadFrom(rb)
		if err != nil {
			return 0, err
		}
		rm, err := icmp.ParseMessage(1, rb[:n])
		if err != nil || rm.Type != ipv4.ICMPTypeEchoReply {
			continue
		}
		echo, ok := rm.Body.(*icmp.Echo)
		// In unprivileged mode the kernel rewrites the echo ID, so match on sequence
		// number and payload instead.
		if !ok || echo.Seq != seq || string(echo.Data) != string(payload) {
			continue
		}
		if !sameIP(peer, ip) {
			continue
		}
		return time.Since(start), nil
	}
}

func sameIP(a net.Addr, ip net.IP) bool {
	switch v := a.(type) {
	case *net.UDPAddr:
		return v.IP.Equal(ip)
	case *net.IPAddr:
		return v.IP.Equal(ip)
	}
	return false
}

func resolve4(ctx context.Context, host string) (net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return v4, nil
		}
		return nil, errors.New("only IPv4 is supported for ping")
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, a := range addrs {
		if v4 := a.IP.To4(); v4 != nil {
			return v4, nil
		}
	}
	return nil, fmt.Errorf("%s has no IPv4 address", host)
}

// TCP connects to host:port.
func TCP(ctx context.Context, host string, port int, timeout time.Duration) (time.Duration, error) {
	d := net.Dialer{Timeout: timeout}
	start := time.Now()
	c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return 0, err
	}
	c.Close()
	return time.Since(start), nil
}

var httpClient = &http.Client{
	Transport: &http.Transport{
		// Homelab services often use self-signed certificates. This only checks that
		// something answers; nothing secret is sent.
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// HTTP requests url and treats any answer below 500 as up (a login page or 401 still
// means the service is running).
func HTTP(ctx context.Context, url string, timeout time.Duration) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	start := time.Now()
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	if resp.StatusCode >= 500 {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return time.Since(start), nil
}

// Run executes one probe against a device host.
func Run(ctx context.Context, host string, p store.ProbeConfig, timeout time.Duration) (time.Duration, error) {
	switch p.Method {
	case "ping":
		return Ping(ctx, host, timeout)
	case "tcp":
		if p.Port <= 0 {
			return 0, errors.New("no port set")
		}
		return TCP(ctx, host, p.Port, timeout)
	case "http":
		u := p.URL
		if u == "" {
			u = "http://" + host + "/"
		}
		return HTTP(ctx, u, timeout)
	case "none", "":
		return 0, nil
	}
	return 0, fmt.Errorf("unknown probe method %q", p.Method)
}

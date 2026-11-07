// Package discovery finds devices on the network. Whatever it finds lands in the
// inbox as "discovered"; nothing is monitored until the user adds it.
package discovery

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/KrX3D/Homewarden/internal/events"
	"github.com/KrX3D/Homewarden/internal/presence"
	"github.com/KrX3D/Homewarden/internal/store"
)

// MaxHosts limits a scan to a /22.
const MaxHosts = 1024

// Ports tried when a host does not answer ping (Windows blocks ICMP by default).
var fallbackPorts = []int{22, 80, 443, 445, 8080}

// Scanner runs one network scan at a time.
type Scanner struct {
	base    context.Context
	st      *store.Store
	events  *events.Broker
	running atomic.Bool
}

// New returns a scanner.
// base outlives single requests, so a scan keeps running after the HTTP call returns.
func New(base context.Context, st *store.Store, br *events.Broker) *Scanner {
	return &Scanner{base: base, st: st, events: br}
}

// ErrBusy means a scan is already running.
var ErrBusy = errors.New("scan already running")

// Hosts expands a CIDR into its usable host addresses.
func Hosts(cidr string) ([]netip.Addr, error) {
	p, err := netip.ParsePrefix(strings.TrimSpace(cidr))
	if err != nil {
		return nil, err
	}
	if !p.Addr().Is4() {
		return nil, errors.New("only IPv4 ranges are supported")
	}
	p = p.Masked()
	size := 1 << (32 - p.Bits())
	if size > MaxHosts+2 {
		return nil, fmt.Errorf("range too large: at most %d addresses", MaxHosts)
	}
	out := []netip.Addr{}
	a := p.Addr()
	for i := 0; i < size; i++ {
		if size > 2 && (i == 0 || i == size-1) {
			a = a.Next()
			continue // network and broadcast address
		}
		out = append(out, a)
		a = a.Next()
	}
	return out, nil
}

// Result summarises a finished scan.
type Result struct {
	Scanned int `json:"scanned"`
	Alive   int `json:"alive"`
	New     int `json:"new"`
}

// Start validates the range and scans it in the background. Progress and the result
// are published as events.
func (s *Scanner) Start(cidr string) (int, error) {
	hosts, err := Hosts(cidr)
	if err != nil {
		return 0, err
	}
	if !s.running.CompareAndSwap(false, true) {
		return 0, ErrBusy
	}
	go func() {
		defer s.running.Store(false)
		res := s.scan(s.base, hosts)
		s.events.Publish("discovery.done", res)
	}()
	return len(hosts), nil
}

// Running reports whether a scan is in progress.
func (s *Scanner) Running() bool { return s.running.Load() }

func alive(ctx context.Context, ip string) bool {
	if _, err := presence.Ping(ctx, ip, 800*time.Millisecond); err == nil {
		return true
	}
	found := make(chan bool, len(fallbackPorts))
	for _, port := range fallbackPorts {
		go func(p int) {
			_, err := presence.TCP(ctx, ip, p, 600*time.Millisecond)
			found <- err == nil
		}(port)
	}
	for range fallbackPorts {
		if <-found {
			return true
		}
	}
	return false
}

func (s *Scanner) scan(ctx context.Context, hosts []netip.Addr) Result {
	var res Result
	res.Scanned = len(hosts)
	var mu sync.Mutex
	var done atomic.Int64
	sem := make(chan struct{}, 64)
	var wg sync.WaitGroup
	for _, a := range hosts {
		ip := a.String()
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				n := done.Add(1)
				if n%32 == 0 {
					s.events.Publish("discovery.progress", map[string]int64{"done": n, "total": int64(len(hosts))})
				}
			}()
			if !alive(ctx, ip) {
				return
			}
			mu.Lock()
			res.Alive++
			mu.Unlock()
			if known, _ := s.st.HostKnown(ctx, ip); known {
				return
			}
			if _, err := s.st.DeviceByExternal(ctx, "scan", ip); err == nil {
				return
			}
			name := ip
			if ownIP(ip) {
				name = "Homewarden (this container)"
			} else {
				rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
				if names, err := net.DefaultResolver.LookupAddr(rctx, ip); err == nil && len(names) > 0 {
					if n := strings.TrimSuffix(names[0], "."); !LooksGenerated(n) {
						name = n
					}
				}
				cancel()
			}
			d := store.Device{
				Name: name, Host: ip, MAC: arpMAC(ip), Status: store.StatusDiscovered, Source: "scan", ExternalID: ip,
				Presence: store.PresenceConfig{Reach: store.ProbeConfig{Method: "ping"}, SettleSeconds: 60, IntervalSeconds: 30},
			}
			if _, err := s.st.CreateDevice(ctx, d); err == nil {
				mu.Lock()
				res.New++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return res
}

// arpMAC reads the MAC of an address from the kernel's ARP table. It is only there
// when Homewarden shares the layer-2 network with the device (host networking or a
// macvlan/br0 setup); behind Docker's NAT bridge it stays empty.
func arpMAC(ip string) string {
	b, err := os.ReadFile("/proc/net/arp")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n")[1:] {
		f := strings.Fields(line)
		if len(f) >= 4 && f[0] == ip && f[3] != "00:00:00:00:00:00" {
			return strings.ToLower(f[3])
		}
	}
	return ""
}

var containerID = regexp.MustCompile(`^[0-9a-f]{12}(\.|$)`)

// LooksGenerated reports names nobody chose: a container id (Docker answers reverse
// lookups with it) or a bare IP address. Such names may be replaced by better ones, e.g.
// a UniFi alias.
func LooksGenerated(name string) bool {
	return name == "" || containerID.MatchString(name) || net.ParseIP(name) != nil
}

// ownIP reports whether ip belongs to this container.
func ownIP(ip string) bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.String() == ip {
			return true
		}
	}
	return false
}

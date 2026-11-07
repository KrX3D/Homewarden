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
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/KrX3D/Homewarden/internal/events"
	"github.com/KrX3D/Homewarden/internal/oui"
	"github.com/KrX3D/Homewarden/internal/presence"
	"github.com/KrX3D/Homewarden/internal/store"
)

// MaxHosts limits one range to a /22.
const MaxHosts = 1024

// MaxTotal limits all ranges of one scan together.
const MaxTotal = 4096

// Ports tried when a host does not answer ping (Windows blocks ICMP by default).
var fallbackPorts = []int{22, 80, 443, 445, 8080}

// Scanner runs one network scan at a time.
type Scanner struct {
	base    context.Context
	st      *store.Store
	events  *events.Broker
	running atomic.Bool
	mu      sync.Mutex
	last    *Result
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

// SplitRanges splits user input on commas, semicolons and whitespace.
func SplitRanges(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\r' || r == '\t'
	})
}

// Expand turns several ranges into one de-duplicated address list.
func Expand(ranges []string) ([]netip.Addr, error) {
	if len(ranges) == 0 {
		return nil, errors.New("no range given")
	}
	seen := map[netip.Addr]bool{}
	out := []netip.Addr{}
	for _, r := range ranges {
		hosts, err := Hosts(r)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", r, err)
		}
		for _, h := range hosts {
			if !seen[h] {
				seen[h] = true
				out = append(out, h)
			}
		}
	}
	if len(out) > MaxTotal {
		return nil, fmt.Errorf("too many addresses: at most %d per scan", MaxTotal)
	}
	return out, nil
}

// Found is one address that answered.
type Found struct {
	IP         string `json:"ip"`
	Name       string `json:"name"`
	MAC        string `json:"mac,omitempty"`
	Vendor     string `json:"vendor,omitempty"`
	DeviceID   int64  `json:"deviceId,omitempty"`
	DeviceName string `json:"deviceName,omitempty"`
	// Status is "new" (just put in the inbox), "inbox" (already waiting there),
	// "known" (an added device), "ignored" or "error".
	Status string `json:"status"`
}

// Result summarises a finished scan.
type Result struct {
	Ranges  []string `json:"ranges"`
	Scanned int      `json:"scanned"`
	Alive   int      `json:"alive"`
	New     int      `json:"new"`
	Found   []Found  `json:"found"`
	At      int64    `json:"at"`
}

// Start validates the ranges and scans them in the background. Progress and the
// result are published as events.
func (s *Scanner) Start(ranges []string) (int, error) {
	hosts, err := Expand(ranges)
	if err != nil {
		return 0, err
	}
	if !s.running.CompareAndSwap(false, true) {
		return 0, ErrBusy
	}
	go func() {
		defer s.running.Store(false)
		res := s.scan(s.base, hosts)
		res.Ranges = ranges
		res.At = time.Now().Unix()
		s.mu.Lock()
		s.last = &res
		s.mu.Unlock()
		s.events.Publish("discovery.done", res)
	}()
	return len(hosts), nil
}

// Running reports whether a scan is in progress.
func (s *Scanner) Running() bool { return s.running.Load() }

// Last returns the most recent scan result since start-up, or nil.
func (s *Scanner) Last() *Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

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
	res := Result{Scanned: len(hosts), Found: []Found{}}
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
			f := s.inspect(ctx, ip)
			mu.Lock()
			res.Alive++
			if f.Status == "new" {
				res.New++
			}
			res.Found = append(res.Found, f)
			mu.Unlock()
		}()
	}
	wg.Wait()
	sort.Slice(res.Found, func(i, j int) bool {
		a, _ := netip.ParseAddr(res.Found[i].IP)
		b, _ := netip.ParseAddr(res.Found[j].IP)
		return a.Less(b)
	})
	return res
}

// inspect names an address that answered and files it: devices Homewarden already
// has are left alone, unknown ones go to the inbox.
func (s *Scanner) inspect(ctx context.Context, ip string) Found {
	f := Found{IP: ip, Name: ip, MAC: arpMAC(ip)}
	f.Vendor = oui.Lookup(f.MAC)
	if ownIP(ip) {
		f.Name = "Homewarden (this container)"
	} else {
		rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		if names, err := net.DefaultResolver.LookupAddr(rctx, ip); err == nil && len(names) > 0 {
			if n := strings.TrimSuffix(names[0], "."); !LooksGenerated(n) {
				f.Name = n
			}
		}
		cancel()
	}
	if d, err := s.st.DeviceByMACOrHost(ctx, f.MAC, ip); err == nil {
		f.DeviceID, f.DeviceName = d.ID, d.Name
		switch d.Status {
		case store.StatusDiscovered:
			f.Status = "inbox"
		case store.StatusIgnored:
			f.Status = "ignored"
		default:
			f.Status = "known"
		}
		return f
	}
	d := store.Device{
		Name: f.Name, Host: ip, MAC: f.MAC, Status: store.StatusDiscovered, Source: "scan", ExternalID: ip,
		Presence: store.PresenceConfig{Reach: store.ProbeConfig{Method: "ping"}, SettleSeconds: 60, IntervalSeconds: 30},
	}
	created, err := s.st.CreateDevice(ctx, d)
	if err != nil {
		f.Status = "error"
		return f
	}
	f.DeviceID, f.DeviceName, f.Status = created.ID, created.Name, "new"
	return f
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

package monitor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"
)

// Resolvers checks (type resolvers) ask every DNS server a host is configured with
// for the internal names the host needs. Resolvers are tried in order or in parallel
// depending on the client, so one public server listed next to the local one (1.1.1.1
// before AdGuard) makes internal names fail now and then; this finds that.
//
// Servers come from the config, or else from the monitor's device: /etc/resolv.conf
// (systemd-resolved's stub 127.0.0.53 is replaced by its upstreams), on Windows the
// adapters' DNS servers. Homewarden then queries each server itself. Down when a server
// does not know a name, or two servers give different addresses for it. A server
// Homewarden cannot reach (VLAN rules) is noted but only fails the check when no
// server could be asked at all. Loopback servers (dnsmasq, Docker's 127.0.0.11) cannot
// be asked from outside and are skipped.

// ResolverScript reads the DNS servers on a Linux device.
const ResolverScript = `cat /etc/resolv.conf 2>/dev/null; echo ---; cat /run/systemd/resolve/resolv.conf 2>/dev/null`

// ResolverScriptWindows reads the DNS servers of a Windows device (agent, PowerShell).
const ResolverScriptWindows = `Get-DnsClientServerAddress | Where-Object { $_.ServerAddresses } | ForEach-Object { $_.ServerAddresses } | Select-Object -Unique`

// ParseResolvers returns the servers from ResolverScript output (or the Windows
// variant: one address per line), in order, without duplicates.
func ParseResolvers(out string) []string {
	main, upstream, _ := strings.Cut(out, "---")
	list := func(s string) []string {
		var r []string
		for _, l := range strings.Split(s, "\n") {
			f := strings.Fields(l)
			if len(f) == 0 || strings.HasPrefix(f[0], "#") || strings.HasPrefix(f[0], ";") {
				continue
			}
			addr := ""
			switch {
			case f[0] == "nameserver" && len(f) > 1:
				addr = f[1]
			case len(f) == 1:
				addr = f[0]
			}
			addr = strings.SplitN(addr, "%", 2)[0] // fe80::1%eth0
			if net.ParseIP(addr) != nil {
				r = append(r, addr)
			}
		}
		return r
	}
	servers := list(main)
	if len(servers) == 1 && servers[0] == "127.0.0.53" {
		if up := list(upstream); len(up) > 0 {
			servers = up
		}
	}
	return uniq(servers)
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// lookupFunc asks one server for one name and returns its addresses.
type lookupFunc func(ctx context.Context, server, name string) ([]string, error)

// dnsLookup asks server directly (no search domains, no fallback to other servers).
func dnsLookup(ctx context.Context, server, name string) ([]string, error) {
	addr := server
	if _, _, err := net.SplitHostPort(server); err != nil {
		addr = net.JoinHostPort(server, "53")
	}
	res := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	ips, err := res.LookupIPAddr(ctx, strings.TrimSuffix(name, ".")+".")
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, ip := range ips {
		out = append(out, ip.IP.String())
	}
	sort.Strings(out)
	return out, nil
}

// lookupProblem sorts a lookup error: "missing" (the server answered, but has no
// address) or "unreachable" (no answer at all).
func lookupProblem(err error) string {
	var de *net.DNSError
	if !errors.As(err, &de) || de.IsTimeout {
		return "unreachable"
	}
	// NXDOMAIN / no records, or SERVFAIL / REFUSED ("server misbehaving"): the server
	// answered without an address.
	if de.IsNotFound || strings.Contains(de.Err, "no such host") || strings.Contains(de.Err, "server misbehaving") {
		return "missing"
	}
	return "unreachable"
}

func loopback(server string) bool {
	host := server
	if h, _, err := net.SplitHostPort(server); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// checkResolvers asks every server for every name.
func checkResolvers(ctx context.Context, servers, names []string, look lookupFunc) Result {
	start := time.Now()
	names = uniq(names)
	if len(names) == 0 {
		return Result{Msg: "no names set"}
	}
	var asked, skipped, unreachable []string
	missing := map[string][]string{}          // server -> names it does not know
	answers := map[string]map[string]string{} // name -> server -> sorted addresses
	for _, s := range uniq(servers) {
		if loopback(s) {
			skipped = append(skipped, s)
			continue
		}
		reached := false
		for _, n := range names {
			ips, err := look(ctx, s, n)
			if err != nil {
				if lookupProblem(err) == "missing" {
					reached = true
					missing[s] = append(missing[s], n)
				}
				continue
			}
			reached = true
			if answers[n] == nil {
				answers[n] = map[string]string{}
			}
			answers[n][s] = strings.Join(ips, " ")
		}
		if reached {
			asked = append(asked, s)
		} else {
			unreachable = append(unreachable, s)
		}
	}
	r := Result{Ms: float64(time.Since(start).Microseconds()) / 1000}
	var problems []string
	for _, s := range asked {
		if m := missing[s]; len(m) > 0 {
			problems = append(problems, fmt.Sprintf("%s does not know %s", s, strings.Join(m, ", ")))
		}
	}
	for _, n := range names {
		seen := map[string][]string{} // addresses -> servers
		order := []string{}
		for _, s := range asked {
			if a, ok := answers[n][s]; ok {
				if seen[a] == nil {
					order = append(order, a)
				}
				seen[a] = append(seen[a], s)
			}
		}
		if len(order) > 1 {
			parts := []string{}
			for _, a := range order {
				parts = append(parts, strings.Join(seen[a], "/")+" = "+a)
			}
			problems = append(problems, n+" differs: "+strings.Join(parts, ", "))
		}
	}
	var notes []string
	if len(unreachable) > 0 {
		notes = append(notes, "no answer from "+strings.Join(unreachable, ", ")+" (from Homewarden)")
	}
	if len(skipped) > 0 {
		notes = append(notes, "skipped local "+strings.Join(skipped, ", "))
	}
	note := ""
	if len(notes) > 0 {
		note = "; " + strings.Join(notes, "; ")
	}
	switch {
	case len(problems) > 0:
		r.Msg = strings.Join(problems, "; ") + note
	case len(asked) == 0 && len(servers) == 0:
		r.Msg = "no DNS servers found"
	case len(asked) == 0:
		r.Msg = "no server could be asked" + note
	default:
		r.OK = true
		r.Msg = fmt.Sprintf("%d names on %s", len(names), strings.Join(asked, ", ")) + note
	}
	return r
}

// resolvers runs a resolvers check: servers from the config, else from the device.
func (r *Runner) resolvers(ctx context.Context, cfg Config, deviceID *int64) Result {
	servers := uniq(cfg.Servers)
	if len(servers) == 0 {
		if deviceID == nil {
			return Result{Msg: "no device and no servers set"}
		}
		if r.Exec == nil {
			return Result{Msg: "remote commands are not available"}
		}
		script := ResolverScript
		if d, err := r.St.Device(ctx, *deviceID); err == nil && d.Kind == "windows" {
			script = ResolverScriptWindows
		}
		out, err := r.Exec(ctx, *deviceID, script, 30*time.Second)
		if err != nil {
			return Result{Msg: "reading the DNS servers: " + err.Error()}
		}
		if servers = ParseResolvers(out); len(servers) == 0 {
			return Result{Msg: "no DNS servers found on the device"}
		}
	}
	look := r.lookup
	if look == nil {
		look = dnsLookup
	}
	return checkResolvers(ctx, servers, cfg.Names, look)
}

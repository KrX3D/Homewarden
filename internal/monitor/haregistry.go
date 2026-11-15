package monitor

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Home Assistant registry monitor (type "haregistry"): compares, per integration, the
// devices Home Assistant keeps in its device registry with the entities of that
// integration that are actually loaded (have a live state). Integrations that keep
// far more devices than they load entities (nmap_tracker or UniFi clients piling up
// over the years) mark the monitor down before the registry grows into the thousands.
// Uses the WebSocket API, since the REST API has no registry access.

// Defaults when the monitor sets none.
const (
	DefaultHARatio      = 3.0 // devices per loaded entity
	DefaultHAMinDevices = 25  // smaller integrations are never flagged
)

type haRegDevice struct {
	ID            string   `json:"id"`
	Name          *string  `json:"name"`
	NameByUser    *string  `json:"name_by_user"`
	ConfigEntries []string `json:"config_entries"`
}

type haRegEntity struct {
	ID            string  `json:"id"`
	EntityID      string  `json:"entity_id"`
	ConfigEntryID *string `json:"config_entry_id"`
	Platform      string  `json:"platform"`
}

type haConfigEntry struct {
	EntryID string `json:"entry_id"`
	Domain  string `json:"domain"`
}

type haState struct {
	EntityID   string `json:"entity_id"`
	Attributes struct {
		Restored bool   `json:"restored"`
		Name     string `json:"friendly_name"`
		ID       string `json:"id"` // automations: their config id
	} `json:"attributes"`
}

// haRegistry is everything the check reads.
type haRegistry struct {
	devices  []haRegDevice
	entities []haRegEntity
	entries  []haConfigEntry // empty when config_entries/get was refused
	states   []haState
}

// haIntegration is the count for one integration domain.
type haIntegration struct {
	Domain   string
	Devices  int
	Nameless int
	Loaded   int
}

func (i haIntegration) ratio() float64 {
	return float64(i.Devices) / float64(max(i.Loaded, 1))
}

// haRegistryCounts groups devices and loaded entities by integration (pure).
func haRegistryCounts(reg haRegistry) []haIntegration {
	domain := map[string]string{} // config entry -> integration
	for _, e := range reg.entries {
		domain[e.EntryID] = e.Domain
	}
	for _, e := range reg.entities { // fallback when config_entries/get was refused
		if e.ConfigEntryID != nil && domain[*e.ConfigEntryID] == "" && e.Platform != "" {
			domain[*e.ConfigEntryID] = e.Platform
		}
	}
	name := func(entry string) string {
		if d := domain[entry]; d != "" {
			return d
		}
		return "config entry " + entry
	}
	by := map[string]*haIntegration{}
	get := func(d string) *haIntegration {
		if by[d] == nil {
			by[d] = &haIntegration{Domain: d}
		}
		return by[d]
	}
	for _, d := range reg.devices {
		seen := map[string]bool{}
		for _, ce := range d.ConfigEntries {
			n := name(ce)
			if seen[n] {
				continue
			}
			seen[n] = true
			i := get(n)
			i.Devices++
			if (d.NameByUser == nil || *d.NameByUser == "") && (d.Name == nil || *d.Name == "") {
				i.Nameless++
			}
		}
	}
	live := map[string]bool{}
	for _, s := range reg.states {
		if !s.Attributes.Restored { // restored = registry placeholder, the integration did not load it
			live[s.EntityID] = true
		}
	}
	for _, e := range reg.entities {
		if e.ConfigEntryID == nil || !live[e.EntityID] {
			continue
		}
		get(name(*e.ConfigEntryID)).Loaded++
	}
	out := make([]haIntegration, 0, len(by))
	for _, i := range by {
		out = append(out, *i)
	}
	sort.Slice(out, func(a, b int) bool {
		if ra, rb := out[a].ratio(), out[b].ratio(); ra != rb {
			return ra > rb
		}
		return out[a].Domain < out[b].Domain
	})
	return out
}

// haRegistryResult decides up/down (pure, unit tested).
func haRegistryResult(reg haRegistry, cfg Config) Result {
	ratio := cfg.MaxRatio
	if ratio <= 0 {
		ratio = DefaultHARatio
	}
	minDev := cfg.MinDevices
	if minDev <= 0 {
		minDev = DefaultHAMinDevices
	}
	ignore := map[string]bool{}
	for _, d := range cfg.IgnoreIntegrations {
		if d = strings.TrimSpace(strings.ToLower(d)); d != "" {
			ignore[d] = true
		}
	}
	var bad []string
	var top *haIntegration
	total := 0
	counts := haRegistryCounts(reg)
	for k, i := range counts {
		total += i.Devices
		if ignore[i.Domain] || i.Devices == 0 {
			continue
		}
		if i.Devices < minDev {
			continue
		}
		if top == nil {
			top = &counts[k]
		}
		if i.ratio() <= ratio {
			continue
		}
		s := fmt.Sprintf("%s %d devices for %d loaded entities", i.Domain, i.Devices, i.Loaded)
		if i.Nameless > 0 {
			s += fmt.Sprintf(" (%d without a name)", i.Nameless)
		}
		bad = append(bad, s)
	}
	if len(bad) > 0 {
		return Result{Msg: short(strings.Join(bad, "; "))}
	}
	msg := fmt.Sprintf("%d devices in %d integrations", total, len(counts))
	if top != nil {
		msg += fmt.Sprintf(", highest %s %.1f devices per loaded entity", top.Domain, top.ratio())
	}
	return Result{OK: true, Msg: msg}
}

// checkHARegistry reads the registries over the WebSocket API.
func (r *Runner) checkHARegistry(ctx context.Context, cfg Config, deviceID *int64) Result {
	start := time.Now()
	t, err := r.haTargetFor(ctx, deviceID)
	if err != nil {
		return Result{Msg: err.Error()}
	}
	to := time.Duration(cfg.Timeout) * time.Second
	if cfg.Timeout <= 0 {
		to = 30 * time.Second // registries with thousands of devices are several MB
	}
	ctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	reg, err := haFetchRegistry(ctx, t)
	ms := float64(time.Since(start).Microseconds()) / 1000
	if err != nil {
		return Result{Ms: ms, Msg: "Home Assistant: " + err.Error()}
	}
	res := haRegistryResult(reg, cfg)
	res.Ms = ms
	return res
}

func haFetchRegistry(ctx context.Context, t haTarget) (haRegistry, error) {
	var reg haRegistry
	w, closer, err := haDial(ctx, t)
	if err != nil {
		return reg, err
	}
	defer closer()
	err = w.run([]*haCmd{
		{typ: "config/device_registry/list", into: &reg.devices},
		{typ: "config/entity_registry/list", into: &reg.entities},
		{typ: "config_entries/get", into: &reg.entries, opt: true}, // needs an admin token
		{typ: "get_states", into: &reg.states},
	})
	return reg, err
}

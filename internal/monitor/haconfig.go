package monitor

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Home Assistant broken references monitor (type "haconfig"): reads the Lovelace
// dashboards (storage and YAML, including button-card templates), automations,
// scripts and dashboard resources over the WebSocket API and checks every entity,
// device and area they name against the registries and live states. Down when
// something points at an entity that no longer exists, a custom card no resource
// provides, or a local resource file that is gone (HTTP 404), e.g. bar-card views and
// button-card templates still using deleted krx_unraid_* sensors.

// haSource is one config that may reference things.
type haSource struct {
	Name   string // "dashboard Server/Disks", "automation Night light"
	Config any
}

type haResource struct {
	URL  string `json:"url"`
	Type string `json:"type"`
}

// haConfigData is everything the check reads.
type haConfigData struct {
	entities  map[string]bool // entity ids in the registry or with a state
	entityUID map[string]bool // registry ids (device triggers use these as entity_id)
	devices   map[string]bool
	areas     map[string]bool // nil when the area list was not readable
	resources []haResource
	resOK     bool           // resource list was readable
	resStatus map[string]int // local resource url -> HTTP status
	sources   []haSource
}

// Strings under these keys are service/action names or paths, never entity ids.
var haSkipKeys = map[string]bool{"service": true, "action": true, "perform_action": true, "url": true,
	"url_path": true, "navigation_path": true, "image": true, "path": true, "icon": true, "service_template": true}

// Entity ids inside any string (templates, JS in button-card): domain.object_id, not
// part of a path, file name, attribute access or longer identifier.
var haEntityRe = regexp.MustCompile(`(?:^|[^A-Za-z0-9_./@-])([a-z_]+)\.([a-z0-9_]+)`)

// Object ids that are really attribute names of a template variable.
var haNotObject = map[string]bool{"entity_id": true, "name": true, "state": true, "attributes": true, "last_changed": true, "last_updated": true}

// Cards whose resource is named differently.
var haCardAlias = map[string]string{"mod-card": "card-mod"}

type haRefs struct {
	entities, entityUIDs, devices, areas, cards map[string]bool
}

func newHARefs() *haRefs {
	return &haRefs{map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}}
}

func isHex32(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// haWalk collects references from a decoded JSON config (pure).
func haWalk(v any, key string, r *haRefs) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if s, ok := val.(string); ok && k == "type" && strings.HasPrefix(s, "custom:") {
				r.cards[strings.TrimSpace(s)] = true
			}
			haWalk(val, k, r)
		}
	case []any:
		for _, e := range x {
			haWalk(e, key, r)
		}
	case string:
		if haSkipKeys[key] || x == "" {
			return
		}
		plain := !strings.ContainsAny(x, "{%[]") // not a template
		switch key {
		case "device_id":
			if plain {
				r.devices[strings.TrimSpace(x)] = true
			}
			return
		case "area_id":
			if plain {
				r.areas[strings.TrimSpace(x)] = true
			}
			return
		case "entity_id":
			if isHex32(x) {
				r.entityUIDs[x] = true
				return
			}
		}
		s := strings.ReplaceAll(x, "states.", "states ") // states.sensor.x in Jinja
		for _, m := range haEntityRe.FindAllStringSubmatchIndex(s, -1) {
			if end := m[1]; end < len(s) && strings.ContainsRune("{(@-", rune(s[end])) {
				continue // template piece, call, mail address, longer name
			}
			if haNotObject[s[m[4]:m[5]]] {
				continue // variable.attribute in a JS template (camera.entity_id)
			}
			r.entities[s[m[2]:m[3]]+"."+s[m[4]:m[5]]] = true
		}
	}
}

// haCardProvided guesses whether a resource provides custom:<name>: the resource URL
// contains the name, or a distinctive part of it (mushroom-light-card -> mushroom,
// grid-layout -> layout). Loose on purpose: a wrong "missing" is worse than a miss.
func haCardProvided(card string, res []haResource) bool {
	name := strings.ToLower(strings.TrimPrefix(card, "custom:"))
	keys := []string{name}
	if a := haCardAlias[name]; a != "" {
		keys = append(keys, a)
	}
	for _, p := range strings.Split(name, "-") {
		if len(p) >= 4 && p != "card" && p != "lovelace" {
			keys = append(keys, p)
		}
	}
	for _, r := range res {
		u := strings.ToLower(r.URL)
		for _, k := range keys {
			if strings.Contains(u, k) {
				return true
			}
		}
	}
	return false
}

// haConfigFindings lists what is broken, one line per source (pure, unit tested).
func haConfigFindings(d haConfigData, cfg Config) []string {
	var ignore []string
	for _, p := range cfg.IgnoreRefs {
		if p = strings.TrimSpace(strings.ToLower(p)); p != "" {
			ignore = append(ignore, p)
		}
	}
	ignored := func(ref string) bool {
		for _, p := range ignore {
			if ok, _ := path.Match(p, strings.ToLower(ref)); ok {
				return true
			}
		}
		return false
	}
	domains := map[string]bool{}
	for id := range d.entities {
		if dom, _, ok := strings.Cut(id, "."); ok {
			domains[dom] = true
		}
	}
	var out []string
	cardsMissing := map[string][]string{} // card -> sources
	for _, src := range d.sources {
		r := newHARefs()
		haWalk(src.Config, "", r)
		var miss []string
		for id := range r.entities {
			dom, _, _ := strings.Cut(id, ".")
			if domains[dom] && !d.entities[id] && !ignored(id) {
				miss = append(miss, id)
			}
		}
		for id := range r.entityUIDs {
			if !d.entityUID[id] && !ignored(id) {
				miss = append(miss, "entity "+id)
			}
		}
		for id := range r.devices {
			if !d.devices[id] && !ignored(id) {
				miss = append(miss, "device "+id)
			}
		}
		if d.areas != nil {
			for id := range r.areas {
				if !d.areas[id] && !ignored(id) {
					miss = append(miss, "area "+id)
				}
			}
		}
		if d.resOK {
			for c := range r.cards {
				if !haCardProvided(c, d.resources) && !ignored(c) {
					cardsMissing[c] = append(cardsMissing[c], src.Name)
				}
			}
		}
		if len(miss) == 0 {
			continue
		}
		sort.Strings(miss)
		line := src.Name + ": " + strings.Join(miss[:min(len(miss), 3)], ", ")
		if len(miss) > 3 {
			line += fmt.Sprintf(" +%d", len(miss)-3)
		}
		out = append(out, line)
	}
	cards := make([]string, 0, len(cardsMissing))
	for c := range cardsMissing {
		cards = append(cards, c)
	}
	sort.Strings(cards)
	for _, c := range cards {
		out = append(out, fmt.Sprintf("%s has no resource (%s)", c, strings.Join(cardsMissing[c], ", ")))
	}
	for _, res := range d.resources {
		if d.resStatus[res.URL] == http.StatusNotFound && !ignored(res.URL) {
			out = append(out, "resource "+res.URL+" not found")
		}
	}
	return out
}

// haConfigResult decides up/down (pure).
func haConfigResult(d haConfigData, cfg Config) Result {
	if bad := haConfigFindings(d, cfg); len(bad) > 0 {
		msg := strings.Join(bad, "; ")
		if len(bad) > 1 {
			msg = fmt.Sprintf("%d broken: %s", len(bad), msg)
		}
		return Result{Msg: short(msg)}
	}
	return Result{OK: true, Msg: fmt.Sprintf("%d dashboards/automations/scripts, no broken references", len(d.sources))}
}

// checkHAConfig reads everything over the WebSocket API.
func (r *Runner) checkHAConfig(ctx context.Context, cfg Config, deviceID *int64) Result {
	start := time.Now()
	t, err := r.haTargetFor(ctx, deviceID)
	if err != nil {
		return Result{Msg: err.Error()}
	}
	to := time.Duration(cfg.Timeout) * time.Second
	if cfg.Timeout <= 0 {
		to = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	d, err := haFetchConfig(ctx, t)
	ms := float64(time.Since(start).Microseconds()) / 1000
	if err != nil {
		return Result{Ms: ms, Msg: "Home Assistant: " + err.Error()}
	}
	res := haConfigResult(d, cfg)
	res.Ms = ms
	return res
}

func haFetchConfig(ctx context.Context, t haTarget) (haConfigData, error) {
	d := haConfigData{entities: map[string]bool{}, entityUID: map[string]bool{}, devices: map[string]bool{}, resStatus: map[string]int{}}
	w, closer, err := haDial(ctx, t)
	if err != nil {
		return d, err
	}
	defer closer()
	var (
		devices  []haRegDevice
		entities []haRegEntity
		states   []haState
		areas    []struct {
			AreaID string `json:"area_id"`
		}
		dashboards []struct {
			URLPath string `json:"url_path"`
			Title   string `json:"title"`
		}
	)
	first := []*haCmd{
		{typ: "config/device_registry/list", into: &devices},
		{typ: "config/entity_registry/list", into: &entities},
		{typ: "get_states", into: &states},
		{typ: "config/area_registry/list", into: &areas, opt: true},
		{typ: "lovelace/dashboards/list", into: &dashboards, opt: true},
		{typ: "lovelace/resources", into: &d.resources, opt: true},
	}
	if err := w.run(first); err != nil {
		return d, err
	}
	for _, e := range entities {
		d.entities[e.EntityID] = true
		d.entityUID[e.ID] = true
	}
	for _, s := range states {
		d.entities[s.EntityID] = true
	}
	for _, x := range devices {
		d.devices[x.ID] = true
	}
	if first[3].err == "" {
		d.areas = map[string]bool{}
		for _, a := range areas {
			d.areas[a.AreaID] = true
		}
	}
	d.resOK = first[5].err == ""

	// Second round: every dashboard config, automation and script config.
	type pending struct {
		name  string
		cmd   *haCmd
		value *any
	}
	var round []pending
	add := func(name, typ string, args map[string]any) {
		v := new(any)
		round = append(round, pending{name, &haCmd{typ: typ, args: args, into: v, opt: true}, v})
	}
	add("dashboard Overview", "lovelace/config", map[string]any{"url_path": nil})
	for _, db := range dashboards {
		name := db.Title
		if name == "" {
			name = db.URLPath
		}
		add("dashboard "+name, "lovelace/config", map[string]any{"url_path": db.URLPath})
	}
	for _, s := range states {
		dom, _, _ := strings.Cut(s.EntityID, ".")
		if dom != "automation" && dom != "script" {
			continue
		}
		name := s.Attributes.Name
		if name == "" {
			name = s.EntityID
		}
		add(dom+" "+name, dom+"/config", map[string]any{"entity_id": s.EntityID})
	}
	cmds := make([]*haCmd, len(round))
	for i, p := range round {
		cmds[i] = p.cmd
	}
	if err := w.run(cmds); err != nil {
		return d, err
	}
	for _, p := range round {
		if p.cmd.err != "" || *p.value == nil {
			continue // auto-generated dashboard (config_not_found) or no stored config
		}
		v := *p.value
		if m, ok := v.(map[string]any); ok && p.cmd.typ != "lovelace/config" {
			if c, ok := m["config"]; ok { // automation/config, script/config answer {config}
				v = c
			}
		}
		d.sources = append(d.sources, haDashboardSources(p.name, v)...)
	}

	// Local resources (/local, /hacsfiles): a deleted card leaves its resource behind.
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: t.insecure}}}
	for _, res := range d.resources {
		if !strings.HasPrefix(res.URL, "/") || strings.HasPrefix(res.URL, "//") {
			continue // external URLs are not Homewarden's to fetch
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, t.base+res.URL, nil)
		if err != nil {
			continue
		}
		if resp, err := client.Do(req); err == nil {
			resp.Body.Close()
			d.resStatus[res.URL] = resp.StatusCode
		}
	}
	return d, nil
}

// haDashboardSources splits a dashboard into its views (named by title) and the rest
// (button_card_templates and other top-level config); other configs stay whole.
func haDashboardSources(name string, v any) []haSource {
	m, ok := v.(map[string]any)
	if !ok || !strings.HasPrefix(name, "dashboard ") {
		return []haSource{{name, v}}
	}
	views, _ := m["views"].([]any)
	rest := map[string]any{}
	for k, x := range m {
		if k != "views" {
			rest[k] = x
		}
	}
	out := []haSource{{name, rest}}
	for i, view := range views {
		vn := fmt.Sprint(i + 1)
		if vm, ok := view.(map[string]any); ok {
			for _, k := range []string{"title", "path"} {
				if s, _ := vm[k].(string); s != "" {
					vn = s
					break
				}
			}
		}
		out = append(out, haSource{name + "/" + vn, view})
	}
	return out
}

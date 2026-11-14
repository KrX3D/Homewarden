package monitor

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/KrX3D/Homewarden/internal/integrations"
	"github.com/KrX3D/Homewarden/internal/store"
)

// Home Assistant entity monitor (type "ha"): reads /api/states of the monitor's
// Home Assistant device (address and access token from its integration setup) and is
// down when a listed entity has been unavailable for at least UnavailableMinutes.
// Entries may use * wildcards (sensor.bitshake_*). Durations come from the entity's
// last_changed against Home Assistant's own clock (the HTTP Date header).

// DefaultUnavailableMinutes is used when the monitor sets none.
const DefaultUnavailableMinutes = 30

// haEntity is the part of a state object the check needs.
type haEntity struct {
	ID          string    `json:"entity_id"`
	State       string    `json:"state"`
	LastChanged time.Time `json:"last_changed"`
	Attributes  struct {
		Name string `json:"friendly_name"`
	} `json:"attributes"`
}

// haTarget is where to reach Home Assistant.
type haTarget struct {
	base, token string
	insecure    bool
}

// haTargetFor reads address and token from a device using the home-assistant
// integration (device settings merged with the integration setup).
func (r *Runner) haTargetFor(ctx context.Context, deviceID *int64) (haTarget, error) {
	if deviceID == nil {
		return haTarget{}, fmt.Errorf("no device set: pick the Home Assistant device")
	}
	d, err := r.St.Device(ctx, *deviceID)
	if err != nil {
		return haTarget{}, err
	}
	if d.Kind != "home-assistant" {
		return haTarget{}, fmt.Errorf("%s does not use the Home Assistant integration", d.Name)
	}
	return haTargetFromDevice(ctx, r.St.WithDefaults(ctx, d), r.Creds)
}

func haTargetFromDevice(ctx context.Context, d store.Device, creds func(context.Context, int64) (string, string, error)) (haTarget, error) {
	set := integrations.Settings(d)
	t := haTarget{}
	t.insecure, _ = set["insecure"].(bool)
	t.base, _ = set["url"].(string)
	if t.base == "" {
		scheme := "http"
		if https, _ := set["https"].(bool); https {
			scheme = "https"
		}
		port := 8123
		if p, ok := set["port"].(float64); ok && p > 0 {
			port = int(p)
		}
		t.base = fmt.Sprintf("%s://%s:%d", scheme, d.Host, port)
	}
	t.base = strings.TrimRight(t.base, "/")
	var id int64
	switch v := set["token"].(type) {
	case float64:
		id = int64(v)
	case string:
		id, _ = strconv.ParseInt(v, 10, 64)
	}
	if id == 0 || creds == nil {
		return t, fmt.Errorf("no access token: set one in Integrations, Home Assistant, Set up")
	}
	_, tok, err := creds(ctx, id)
	if err != nil {
		return t, fmt.Errorf("access token: %v", err)
	}
	t.token = tok
	return t, nil
}

// checkHA fetches all states and evaluates them.
func (r *Runner) checkHA(ctx context.Context, cfg Config, deviceID *int64) Result {
	start := time.Now()
	t, err := r.haTargetFor(ctx, deviceID)
	if err != nil {
		return Result{Msg: err.Error()}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout(cfg))
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.base+"/api/states", nil)
	if err != nil {
		return Result{Msg: err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+t.token)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: t.insecure}}}
	resp, err := client.Do(req)
	if err != nil {
		return Result{Msg: "Home Assistant: " + err.Error()}
	}
	defer resp.Body.Close()
	ms := float64(time.Since(start).Microseconds()) / 1000
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return Result{Ms: ms, Msg: fmt.Sprintf("Home Assistant refused the access token (HTTP %d)", resp.StatusCode)}
	}
	if resp.StatusCode != http.StatusOK {
		return Result{Ms: ms, Msg: fmt.Sprintf("Home Assistant: HTTP %d", resp.StatusCode)}
	}
	var states []haEntity
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&states); err != nil {
		return Result{Ms: ms, Msg: "Home Assistant: " + err.Error()}
	}
	now := time.Now()
	if d, err := http.ParseTime(resp.Header.Get("Date")); err == nil {
		now = d // HA's clock, so skew between the hosts does not matter
	}
	res := haResult(states, cfg, now)
	res.Ms = ms
	return res
}

// haResult decides up/down from the states (pure, unit tested).
func haResult(states []haEntity, cfg Config, now time.Time) Result {
	pats := []string{}
	for _, e := range cfg.Entities {
		if e = strings.TrimSpace(strings.ToLower(e)); e != "" {
			pats = append(pats, e)
		}
	}
	if len(pats) == 0 {
		return Result{Msg: "no entities set"}
	}
	limit := time.Duration(cfg.UnavailableMinutes) * time.Minute
	if cfg.UnavailableMinutes <= 0 {
		limit = DefaultUnavailableMinutes * time.Minute
	}
	bad := func(s string) bool { return s == "unavailable" || cfg.CountUnknown && s == "unknown" }
	var missing, down, waiting []string
	checked := 0
	for _, p := range pats {
		found := 0
		for _, s := range states {
			if ok, _ := path.Match(p, s.ID); !ok {
				continue
			}
			found++
			checked++
			if !bad(s.State) {
				continue
			}
			age := now.Sub(s.LastChanged)
			label := s.ID
			if s.Attributes.Name != "" {
				label = s.Attributes.Name + " (" + s.ID + ")"
			}
			if s.LastChanged.IsZero() || age >= limit {
				down = append(down, fmt.Sprintf("%s %s for %s", label, s.State, Age(age)))
			} else {
				waiting = append(waiting, s.ID)
			}
		}
		if found == 0 {
			missing = append(missing, p)
		}
	}
	sort.Strings(down)
	switch {
	case len(down) > 0:
		msg := strings.Join(down, "; ")
		if len(down) > 1 {
			msg = fmt.Sprintf("%d unavailable: %s", len(down), msg)
		}
		return Result{Msg: short(msg)}
	case len(missing) > 0:
		return Result{Msg: short("not found: " + strings.Join(missing, ", "))}
	case len(waiting) > 0:
		return Result{OK: true, Msg: fmt.Sprintf("%d of %d available, %d briefly unavailable", checked-len(waiting), checked, len(waiting))}
	}
	return Result{OK: true, Msg: fmt.Sprintf("%d available", checked)}
}

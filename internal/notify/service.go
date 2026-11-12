package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/KrX3D/Homewarden/internal/events"
	"github.com/KrX3D/Homewarden/internal/store"
	"github.com/KrX3D/Homewarden/internal/vault"
)

// Event names a channel can subscribe to.
const (
	EvUpdates  = "updates"  // new updates found
	EvSecurity = "security" // new security updates only
	EvJobs     = "jobs"     // failed or blocked jobs
	EvWarnings = "warnings" // device warnings (disks, undervoltage, old backups, end of life)
	EvOffline  = "offline"  // device offline longer than its limit (per-device opt-in)
	EvMonitors = "monitors" // a monitor went down or came back up
)

// Events lists all event names in display order.
var Events = []string{EvUpdates, EvSecurity, EvJobs, EvWarnings, EvOffline, EvMonitors}

// Service turns server events into notifications.
type Service struct {
	St     *store.Store
	Vault  *vault.Vault
	Events *events.Broker

	mu      sync.Mutex
	offline map[int64]*time.Timer
	hb      HeartbeatStatus
}

// Settings are global notification settings.
type Settings struct {
	Lang    string `json:"lang"`    // en | de
	BaseURL string `json:"baseUrl"` // for links in messages
}

// LoadSettings reads them.
func LoadSettings(ctx context.Context, st *store.Store) Settings {
	raw, _ := st.GetSetting(ctx, "notify", "")
	var s Settings
	_ = json.Unmarshal([]byte(raw), &s)
	if s.Lang != "de" {
		s.Lang = "en"
	}
	return s
}

func (s *Service) text(lang, en, de string) string {
	if lang == "de" {
		return de
	}
	return en
}

// Dispatch sends a message to every enabled channel subscribed to event.
func (s *Service) Dispatch(ctx context.Context, event string, m Message) {
	chans, err := s.St.NotifyChannels(ctx)
	if err != nil {
		return
	}
	m.Event = event
	for _, c := range chans {
		if !c.Enabled || !contains(c.Events, event) {
			continue
		}
		cfg, err := s.DecryptConfig(c)
		if err != nil {
			log.Printf("notify %s: %v", c.Name, err)
			continue
		}
		if err := Send(ctx, c.Type, cfg, m); err != nil {
			log.Printf("notify %s: %v", c.Name, err)
		}
	}
}

// DecryptConfig opens a channel's stored config.
func (s *Service) DecryptConfig(c store.NotifyChannel) (Config, error) {
	var cfg Config
	plain, err := s.Vault.DecryptString(c.Config)
	if err != nil {
		return cfg, err
	}
	err = json.Unmarshal([]byte(plain), &cfg)
	return cfg, err
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Run follows server events until ctx ends.
func (s *Service) Run(ctx context.Context) {
	s.offline = map[int64]*time.Timer{}
	ch := s.Events.Subscribe()
	defer s.Events.Unsubscribe(ch)
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-ch:
			b, _ := json.Marshal(ev.Data)
			var x struct {
				ID       int64  `json:"id"`
				Status   string `json:"status"`
				State    string `json:"state"`
				Previous string `json:"previous"`
			}
			_ = json.Unmarshal(b, &x)
			switch ev.Type {
			case "device.software":
				s.onSoftware(ctx, x.ID)
			case "job.status":
				if x.Status == store.JobFailed || x.Status == store.JobBlocked {
					s.onJob(ctx, x.ID)
				}
			case "device.state":
				s.onState(ctx, x.ID, x.State)
			case "device.metrics":
				var mv struct {
					Warnings []string `json:"warnings"`
				}
				_ = json.Unmarshal(b, &mv)
				s.onMetricWarnings(ctx, x.ID, mv.Warnings)
			case "monitor.state":
				var mv struct {
					Name     string `json:"name"`
					Status   string `json:"status"`
					Previous string `json:"previous"`
					Msg      string `json:"msg"`
					DeviceID *int64 `json:"deviceId"`
					Repeat   bool   `json:"repeat"`
				}
				_ = json.Unmarshal(b, &mv)
				s.onMonitor(ctx, x.ID, mv.Name, mv.Status, mv.Previous, mv.Msg, mv.DeviceID, mv.Repeat)
			case "monitor.cert":
				var mv struct {
					Name     string `json:"name"`
					Expires  int64  `json:"expires"`
					Level    int    `json:"level"`
				}
				_ = json.Unmarshal(b, &mv)
				s.onCert(ctx, x.ID, mv.Name, mv.Expires, mv.Level)
			}
		}
	}
}

func (s *Service) link(set Settings, deviceID int64) string {
	if set.BaseURL == "" {
		return ""
	}
	return strings.TrimRight(set.BaseURL, "/") + fmt.Sprintf("/devices/%d", deviceID)
}

func (s *Service) onSoftware(ctx context.Context, deviceID int64) {
	d, err := s.St.Device(ctx, deviceID)
	if err != nil || d.Status != store.StatusActive {
		return
	}
	set := LoadSettings(ctx, s.St)
	comps, err := s.St.Components(ctx, deviceID)
	if err != nil {
		return
	}
	var fresh, freshSec []string
	for _, c := range comps {
		// Summary rows ("46 packages") would only repeat the individual updates.
		if !c.UpdateAvailable || (c.Group == "os" && c.Available == "") {
			continue
		}
		key := fmt.Sprintf("upd:%d:%s:%s", deviceID, c.ID, c.Available)
		if ok, _ := s.St.MarkSent(ctx, key); !ok {
			continue
		}
		label := c.Name
		if c.Available != "" {
			label += " " + c.Available
		}
		fresh = append(fresh, label)
		if c.Security {
			freshSec = append(freshSec, label)
		}
	}
	list := func(items []string) string {
		if len(items) > 10 {
			return "- " + strings.Join(items[:10], "\n- ") + fmt.Sprintf("\n… +%d", len(items)-10)
		}
		return "- " + strings.Join(items, "\n- ")
	}
	if len(fresh) > 0 {
		s.Dispatch(ctx, EvUpdates, Message{Title: fmt.Sprintf(s.text(set.Lang, "%s: %d new updates", "%s: %d neue Updates"), d.Name, len(fresh)),
			Body: list(fresh), Link: s.link(set, d.ID)})
	}
	if len(freshSec) > 0 {
		s.Dispatch(ctx, EvSecurity, Message{Title: fmt.Sprintf(s.text(set.Lang, "%s: %d security updates", "%s: %d Sicherheitsupdates"), d.Name, len(freshSec)),
			Body: list(freshSec), Priority: 1, Link: s.link(set, d.ID)})
	}

	// Warnings: notify once while a warning is present; clear when it goes away.
	f, err := s.St.DeviceFacts(ctx, deviceID)
	if err != nil {
		return
	}
	var facts struct {
		Warnings  []string `json:"warnings"`
		EOLPassed bool     `json:"eolPassed"`
	}
	_ = json.Unmarshal(f.Facts, &facts)
	if facts.EOLPassed {
		facts.Warnings = append(facts.Warnings, "eol")
	}
	present := map[string]bool{}
	for _, w := range facts.Warnings {
		present[w] = true
		if ok, _ := s.St.MarkSent(ctx, fmt.Sprintf("warn:%d:%s:", deviceID, w)); ok {
			s.Dispatch(ctx, EvWarnings, Message{Title: fmt.Sprintf(s.text(set.Lang, "%s: warning", "%s: Warnung"), d.Name),
				Body: warningText(set.Lang, w), Priority: 1, Link: s.link(set, d.ID)})
		}
	}
	for _, w := range []string{"undervoltage", "disk_problems", "datastore_full", "backup_old", "backup_errors", "unifi_offline", "eol"} {
		if !present[w] {
			_ = s.St.ClearSent(ctx, fmt.Sprintf("warn:%d:%s:", deviceID, w))
		}
	}
}

func warningText(lang, w string) string {
	en := map[string]string{
		"undervoltage": "The Pi reported undervoltage.", "disk_problems": "A disk reports errors, is disabled or runs hot.",
		"datastore_full": "A backup datastore is almost full.", "backup_old": "The newest backup is older than the limit.",
		"backup_errors": "Backup tasks failed in the last 7 days.", "unifi_offline": "UniFi devices are offline.",
		"eol":       "The operating system no longer receives updates.",
		"disk_full": "A file system is more than 90% full.", "hot": "A temperature sensor reports 80 °C or more.",
		"smart_failing": "A disk's SMART self-assessment failed. Replace it soon.",
	}
	de := map[string]string{
		"undervoltage": "Der Pi hat Unterspannung gemeldet.", "disk_problems": "Eine Platte meldet Fehler, ist deaktiviert oder zu heiß.",
		"datastore_full": "Ein Backup-Datastore ist fast voll.", "backup_old": "Das neueste Backup ist älter als die Grenze.",
		"backup_errors": "Backup-Tasks sind in den letzten 7 Tagen fehlgeschlagen.", "unifi_offline": "UniFi-Geräte sind offline.",
		"eol":       "Das Betriebssystem bekommt keine Updates mehr.",
		"disk_full": "Ein Dateisystem ist zu mehr als 90 % voll.", "hot": "Ein Temperatursensor meldet 80 °C oder mehr.",
		"smart_failing": "Die SMART-Selbstbewertung einer Platte ist fehlgeschlagen. Bald ersetzen.",
	}
	m := en
	if lang == "de" {
		m = de
	}
	if t, ok := m[w]; ok {
		return t
	}
	return w
}

func (s *Service) onJob(ctx context.Context, jobID int64) {
	j, err := s.St.Job(ctx, jobID)
	if err != nil {
		return
	}
	set := LoadSettings(ctx, s.St)
	lines := strings.Split(strings.TrimSpace(j.Log), "\n")
	if len(lines) > 6 {
		lines = lines[len(lines)-6:]
	}
	title := fmt.Sprintf(s.text(set.Lang, "Failed: %s", "Fehlgeschlagen: %s"), j.Title)
	if j.Status == store.JobBlocked {
		title = fmt.Sprintf(s.text(set.Lang, "Blocked: %s", "Blockiert: %s"), j.Title)
	}
	link := ""
	if set.BaseURL != "" {
		link = strings.TrimRight(set.BaseURL, "/") + fmt.Sprintf("/activity/%d", j.ID)
	}
	s.Dispatch(ctx, EvJobs, Message{Title: title, Body: strings.Join(lines, "\n"), Priority: 1, Link: link})
}

// onState starts a timer when a device with an offline limit goes offline, and
// reports "back online" when an offline notification was sent.
func (s *Service) onState(ctx context.Context, deviceID int64, state string) {
	d, err := s.St.Device(ctx, deviceID)
	if err != nil {
		return
	}
	var cfg struct {
		Notify struct {
			OfflineMinutes int `json:"offlineMinutes"`
		} `json:"notify"`
	}
	_ = json.Unmarshal(d.Config, &cfg)
	limit := cfg.Notify.OfflineMinutes
	s.mu.Lock()
	if t, ok := s.offline[deviceID]; ok {
		t.Stop()
		delete(s.offline, deviceID)
	}
	s.mu.Unlock()
	set := LoadSettings(ctx, s.St)
	key := fmt.Sprintf("off:%d:", deviceID)
	if state != store.StateOffline {
		// "down" exists only if we reported this device offline; then say it is back.
		inserted, _ := s.St.MarkSent(ctx, key+"down")
		_ = s.St.ClearSent(ctx, key)
		if !inserted {
			s.Dispatch(ctx, EvOffline, Message{Title: fmt.Sprintf(s.text(set.Lang, "%s is back online", "%s ist wieder online"), d.Name), Link: s.link(set, d.ID)})
		}
		return
	}
	if limit <= 0 || d.Status != store.StatusActive || s.St.InMaintenance(ctx, deviceID) {
		return
	}
	s.mu.Lock()
	s.offline[deviceID] = time.AfterFunc(time.Duration(limit)*time.Minute, func() {
		cur, err := s.St.Device(ctx, deviceID)
		if err != nil || cur.State != store.StateOffline {
			return
		}
		if ok, _ := s.St.MarkSent(ctx, key+"down"); ok {
			s.Dispatch(ctx, EvOffline, Message{Title: fmt.Sprintf(s.text(set.Lang, "%s is offline", "%s ist offline"), d.Name),
				Body: fmt.Sprintf(s.text(set.Lang, "No answer for %d minutes.", "Seit %d Minuten keine Antwort."), limit), Priority: 1, Link: s.link(set, d.ID)})
		}
	})
	s.mu.Unlock()
}

// onMonitor reports a monitor going down (again every few checks when repeat is set),
// and coming back after it was reported down. "dependency" (parent down) sends nothing.
// Nothing is sent during a maintenance window.
func (s *Service) onMonitor(ctx context.Context, id int64, name, status, previous, msg string, deviceID *int64, repeat bool) {
	dev := int64(0)
	if deviceID != nil {
		dev = *deviceID
	}
	if s.St.InMaintenance(ctx, dev) {
		return
	}
	set := LoadSettings(ctx, s.St)
	link := ""
	if set.BaseURL != "" {
		link = strings.TrimRight(set.BaseURL, "/") + "/monitors"
	}
	if status == "down" && repeat {
		s.Dispatch(ctx, EvMonitors, Message{Title: fmt.Sprintf(s.text(set.Lang, "%s is still down", "%s ist noch immer ausgefallen"), name), Body: msg, Priority: 1, Link: link})
	} else if status == "down" {
		s.Dispatch(ctx, EvMonitors, Message{Title: fmt.Sprintf(s.text(set.Lang, "%s is down", "%s ist ausgefallen"), name), Body: msg, Priority: 1, Link: link})
	} else if status == "up" && previous == "down" {
		s.Dispatch(ctx, EvMonitors, Message{Title: fmt.Sprintf(s.text(set.Lang, "%s is up again", "%s ist wieder erreichbar"), name), Body: msg, Link: link})
	}
}

// onCert reports a certificate that expires within a monitor's warning days, once per
// threshold and certificate (a renewed certificate has a new expiry date).
func (s *Service) onCert(ctx context.Context, id int64, name string, expires int64, level int) {
	if ok, _ := s.St.MarkSent(ctx, fmt.Sprintf("cert:%d:%d:%d", id, level, expires)); !ok {
		return
	}
	set := LoadSettings(ctx, s.St)
	link := ""
	if set.BaseURL != "" {
		link = strings.TrimRight(set.BaseURL, "/") + "/monitors"
	}
	end := time.Unix(expires, 0)
	days := int(time.Until(end).Hours() / 24)
	var title string
	if days < 0 {
		title = fmt.Sprintf(s.text(set.Lang, "Certificate of %s has expired", "Zertifikat von %s ist abgelaufen"), name)
	} else {
		title = fmt.Sprintf(s.text(set.Lang, "Certificate of %s expires in %d days", "Zertifikat von %s läuft in %d Tagen ab"), name, days)
	}
	s.Dispatch(ctx, EvMonitors, Message{Title: title, Body: end.Format("2006-01-02"), Link: link})
}

// onMetricWarnings reports warnings from resource samples once while they last.
func (s *Service) onMetricWarnings(ctx context.Context, deviceID int64, warnings []string) {
	d, err := s.St.Device(ctx, deviceID)
	if err != nil || d.Status != store.StatusActive || s.St.InMaintenance(ctx, deviceID) {
		return
	}
	set := LoadSettings(ctx, s.St)
	present := map[string]bool{}
	for _, w := range warnings {
		present[w] = true
		if ok, _ := s.St.MarkSent(ctx, fmt.Sprintf("mwarn:%d:%s:", deviceID, w)); ok {
			s.Dispatch(ctx, EvWarnings, Message{Title: fmt.Sprintf(s.text(set.Lang, "%s: warning", "%s: Warnung"), d.Name),
				Body: warningText(set.Lang, w), Priority: 1, Link: s.link(set, d.ID)})
		}
	}
	for _, w := range []string{"disk_full", "hot", "smart_failing"} {
		if !present[w] {
			_ = s.St.ClearSent(ctx, fmt.Sprintf("mwarn:%d:%s:", deviceID, w))
		}
	}
}

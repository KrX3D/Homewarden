package api

import (
	"encoding/json"
	"sort"
)

// Overview data from integration facts: AdGuard Home (DNS), UniFi (internet), Nginx
// Proxy Manager (proxy hosts, certificates), Proxmox Backup Server (backup groups),
// Home Assistant (low batteries) and Gitea (open pull requests).

type ovDNS struct {
	Instances int       `json:"instances"`
	Queries   int64     `json:"queries"`
	Blocked   int64     `json:"blocked"`
	AvgMs     *float64  `json:"avgMs"`
	Series    []float64 `json:"series"`
	InSync    bool      `json:"inSync"`
	Off       []string  `json:"off"` // instances with protection off
}

type ovInternet struct {
	Device      string   `json:"device"`
	Status      string   `json:"status"`
	ISP         string   `json:"isp"`
	Down        *float64 `json:"down"`
	Up          *float64 `json:"up"`
	Latency     *float64 `json:"latency"`
	SpeedtestAt *int64   `json:"speedtestAt"`
}

type ovProxy struct {
	Enabled int   `json:"enabled"`
	Offline int   `json:"offline"`
	Certs   int   `json:"certs"`
	Soonest int64 `json:"soonest"`
}

type ovPBSGroup struct {
	Server  string `json:"server"`
	Store   string `json:"store"`
	Type    string `json:"type"`
	ID      string `json:"id"`
	Comment string `json:"comment"`
	Last    int64  `json:"last"`
	Count   int    `json:"count"`
	Verify  string `json:"verify"`
	Fresh   bool   `json:"fresh"`
}

type ovPBS struct {
	Groups   int   `json:"groups"`
	Fresh    int   `json:"fresh"`
	Verified int   `json:"verified"`
	Failed   int   `json:"failed"`
	Last     int64 `json:"last"`
}

type ovBattery struct {
	Name     string   `json:"name"`
	Level    *float64 `json:"level"`
	DeviceID int64    `json:"deviceId"`
}

type ovPR struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	Author string `json:"author"`
	Bot    bool   `json:"bot"`
	URL    string `json:"url"`
	Forge  string `json:"forge"`
}

type ovPRs struct {
	Count int    `json:"count"`
	Bots  int    `json:"bots"`
	Items []ovPR `json:"items"`
}

// sources collects across devices; finish() fills the overview.
type sources struct {
	dns      *ovDNS
	dnsKeys  map[string]bool
	internet *ovInternet
	proxy    *ovProxy
	pbs      *ovPBS
	certs    []ovCert
	bats     []ovBattery
	prs      ovPRs
}

func (x *sources) add(kind, name string, id int64, raw json.RawMessage, now int64) {
	switch kind {
	case "adguard":
		var f struct {
			Protection bool      `json:"protection"`
			Queries    int64     `json:"queries"`
			Blocked    int64     `json:"blocked"`
			AvgMs      *float64  `json:"avgMs"`
			Series     []float64 `json:"queriesSeries"`
			SyncKey    string    `json:"syncKey"`
		}
		if json.Unmarshal(raw, &f) != nil || f.SyncKey == "" {
			return
		}
		if x.dns == nil {
			x.dns = &ovDNS{Off: []string{}, Series: []float64{}}
			x.dnsKeys = map[string]bool{}
		}
		d := x.dns
		d.Instances++
		d.Queries += f.Queries
		d.Blocked += f.Blocked
		if f.AvgMs != nil && (d.AvgMs == nil || *f.AvgMs > *d.AvgMs) {
			d.AvgMs = f.AvgMs
		}
		for i, v := range f.Series {
			if i < len(d.Series) {
				d.Series[i] += v
			} else {
				d.Series = append(d.Series, v)
			}
		}
		x.dnsKeys[f.SyncKey] = true
		if !f.Protection {
			d.Off = append(d.Off, name)
		}
	case "unifi":
		var f struct {
			Wan *struct {
				Status      string   `json:"status"`
				ISP         string   `json:"isp"`
				Down        *float64 `json:"down"`
				Up          *float64 `json:"up"`
				Latency     *float64 `json:"latency"`
				SpeedtestAt *int64   `json:"speedtestAt"`
			} `json:"wan"`
		}
		if json.Unmarshal(raw, &f) == nil && f.Wan != nil && x.internet == nil {
			x.internet = &ovInternet{name, f.Wan.Status, f.Wan.ISP, f.Wan.Down, f.Wan.Up, f.Wan.Latency, f.Wan.SpeedtestAt}
		}
	case "nginx-proxy-manager":
		var f struct {
			Enabled int `json:"enabledHosts"`
			Offline int `json:"offlineHosts"`
			Certs   []struct {
				Name    string `json:"name"`
				Expires int64  `json:"expires"`
			} `json:"certs"`
		}
		if json.Unmarshal(raw, &f) != nil {
			return
		}
		if x.proxy == nil {
			x.proxy = &ovProxy{}
		}
		x.proxy.Enabled += f.Enabled
		x.proxy.Offline += f.Offline
		for _, c := range f.Certs {
			if c.Expires == 0 {
				continue
			}
			x.proxy.Certs++
			if x.proxy.Soonest == 0 || c.Expires < x.proxy.Soonest {
				x.proxy.Soonest = c.Expires
			}
			if c.Expires-now < 30*86400 {
				x.certs = append(x.certs, ovCert{Name: c.Name, Expires: c.Expires, DeviceID: id})
			}
		}
	case "proxmox-backup":
		var f struct {
			MaxAgeHours float64 `json:"maxAgeHours"`
			Groups      []struct {
				Store   string `json:"store"`
				Type    string `json:"type"`
				ID      string `json:"id"`
				Comment string `json:"comment"`
				Last    int64  `json:"last"`
				Count   int    `json:"count"`
				Verify  string `json:"verify"`
			} `json:"backupGroups"`
		}
		if json.Unmarshal(raw, &f) != nil || f.Groups == nil {
			return
		}
		if f.MaxAgeHours <= 0 {
			f.MaxAgeHours = 48
		}
		if x.pbs == nil {
			x.pbs = &ovPBS{}
		}
		for _, g := range f.Groups {
			x.pbs.Groups++
			if float64(now-g.Last) < f.MaxAgeHours*3600 {
				x.pbs.Fresh++
			}
			switch g.Verify {
			case "ok":
				x.pbs.Verified++
			case "failed":
				x.pbs.Failed++
			}
			x.pbs.Last = max(x.pbs.Last, g.Last)
		}
	case "home-assistant":
		var f struct {
			Low []struct {
				Name  string   `json:"name"`
				Level *float64 `json:"level"`
			} `json:"lowBatteries"`
		}
		if json.Unmarshal(raw, &f) == nil {
			for _, b := range f.Low {
				x.bats = append(x.bats, ovBattery{b.Name, b.Level, id})
			}
		}
	case "gitea":
		var f struct {
			PRs []ovPR `json:"pullRequests"`
		}
		if json.Unmarshal(raw, &f) == nil {
			for _, p := range f.PRs {
				x.prs.Count++
				if p.Bot {
					x.prs.Bots++
				}
				x.prs.Items = append(x.prs.Items, p)
			}
		}
	}
}

// finish writes the collected data into the overview parts.
func (x *sources) finish(chores *ovChores, today *ovToday, problems *[]ovProblem) {
	if x.dns != nil {
		x.dns.InSync = len(x.dnsKeys) <= 1
		today.DNS = x.dns
		if !x.dns.InSync {
			*problems = append(*problems, ovProblem{Kind: "warning", Severity: "warn", Name: "AdGuard Home", Area: AreaNetwork, Code: "adguard_out_of_sync"})
		}
	}
	today.Internet, today.Proxy, today.PBS = x.internet, x.proxy, x.pbs
	chores.Certs = append(chores.Certs, x.certs...)
	sort.Slice(chores.Certs, func(i, j int) bool { return chores.Certs[i].Expires < chores.Certs[j].Expires })
	if x.bats != nil {
		chores.Batteries = x.bats
	}
	if len(x.prs.Items) > 8 {
		x.prs.Items = x.prs.Items[:8]
	}
	if x.prs.Items == nil {
		x.prs.Items = []ovPR{}
	}
	chores.PRs = x.prs
}

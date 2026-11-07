// Package integrations loads, installs and runs integrations: a manifest plus a
// JavaScript file executed in a sandbox that only gets what the core grants.
package integrations

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/KrX3D/Homewarden/internal/store"
)

// Text is a translated string: {"en": "...", "de": "..."}.
type Text map[string]string

// Option is one choice of a select setting.
type Option struct {
	Value string `json:"value"`
	Label Text   `json:"label"`
}

// Setting is one field of the per-device settings form the GUI renders.
type Setting struct {
	Key             string   `json:"key"`
	Type            string   `json:"type"` // string | number | bool | select | credential | password
	Label           Text     `json:"label"`
	Help            Text     `json:"help,omitempty"`
	Default         any      `json:"default,omitempty"`
	Required        bool     `json:"required,omitempty"`
	Options         []Option `json:"options,omitempty"`
	CredentialTypes []string `json:"credentialTypes,omitempty"`
	Placeholder     string   `json:"placeholder,omitempty"`
	// Scope "device" marks values that differ per device (an address, a node name).
	// They are set in the device's Integration card only; all others are shared in the
	// integration setup and can be overridden per device.
	Scope string `json:"scope,omitempty"`
}

// Port is one network requirement. Target: device (Homewarden reaches the device),
// internet (Homewarden reaches the internet), homewarden (the device reaches Homewarden).
type Port struct {
	Target string `json:"target"`
	Proto  string `json:"proto"`
	Port   string `json:"port"`
	Note   Text   `json:"note"`
}

// Permissions is what the sandbox lets the script do. Shown before installing.
type Permissions struct {
	SSH        bool     `json:"ssh"`
	Docker     bool     `json:"docker"`
	HTTPDevice bool     `json:"httpDevice"` // HTTP to the device's own address
	HTTPHosts  []string `json:"httpHosts"`  // further hosts (exact or *.suffix)
	// HTTPHostsSetting names a device setting whose comma-separated hosts are allowed
	// too (e.g. a private registry). The user types those hosts, so they are theirs.
	HTTPHostsSetting string `json:"httpHostsSetting,omitempty"`
}

// Manifest describes an integration.
type Manifest struct {
	ID           string                `json:"id"`
	Version      string                `json:"version"`
	MinCore      string                `json:"minCore,omitempty"`
	Name         Text                  `json:"name"`
	Description  Text                  `json:"description"`
	Howto        Text                  `json:"howto,omitempty"`
	Connection   string                `json:"connection"` // ssh | http | docker | none
	Permissions  Permissions           `json:"permissions"`
	Settings     []Setting             `json:"settings,omitempty"`
	Presence     *store.PresenceConfig `json:"presence,omitempty"`
	Capabilities []string              `json:"capabilities"` // detect, update, discover, busy, backup
	Entry        string                `json:"entry"`
	Files        []string              `json:"files"`
	Icon         string                `json:"icon,omitempty"`
	// Ports lists the network access the integration needs, shown in the GUI so
	// firewall/VLAN rules can be set up.
	Ports []Port `json:"ports,omitempty"`
	// Sudoers is the rule Install key can add for updates ($USER is replaced by the
	// login). Lets a fresh device be set up from the GUI without a terminal.
	Sudoers string `json:"sudoers,omitempty"`
}

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,40}$`)
var versionRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
var fileRe = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// Has reports whether the manifest declares a capability.
func (m Manifest) Has(cap string) bool {
	for _, c := range m.Capabilities {
		if c == cap {
			return true
		}
	}
	return false
}

// ParseManifest reads and validates a manifest.
func ParseManifest(b []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("manifest: %w", err)
	}
	switch {
	case !idRe.MatchString(m.ID):
		return m, errors.New("manifest: invalid id")
	case !versionRe.MatchString(m.Version):
		return m, errors.New("manifest: version must be x.y.z")
	case m.Name["en"] == "":
		return m, errors.New("manifest: name.en is required")
	case m.Entry == "":
		return m, errors.New("manifest: entry is required")
	}
	hasEntry := false
	for _, f := range m.Files {
		if !fileRe.MatchString(f) || strings.HasPrefix(f, ".") {
			return m, fmt.Errorf("manifest: bad file name %q", f)
		}
		if f == m.Entry {
			hasEntry = true
		}
	}
	if !hasEntry {
		return m, errors.New("manifest: entry must be listed in files")
	}
	for _, s := range m.Settings {
		if s.Key == "" || s.Key == "connection" {
			return m, errors.New("manifest: setting needs a key other than connection")
		}
	}
	return m, nil
}

// CompareVersions compares dotted numeric versions (1.10.0 > 1.9.3). Non-numeric
// parts compare as text. Returns -1, 0 or 1.
func CompareVersions(a, b string) int {
	norm := func(s string) []string {
		s = strings.TrimPrefix(strings.TrimSpace(s), "v")
		return strings.FieldsFunc(s, func(r rune) bool { return r == '.' || r == '-' || r == '+' || r == '~' || r == ':' })
	}
	pa, pb := norm(a), norm(b)
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y string
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x == y {
			continue
		}
		xn, xerr := atoi(x)
		yn, yerr := atoi(y)
		switch {
		case xerr == nil && yerr == nil:
			if xn < yn {
				return -1
			}
			return 1
		case x == "":
			return -1
		case y == "":
			return 1
		case x < y:
			return -1
		default:
			return 1
		}
	}
	return 0
}

func atoi(s string) (int, error) {
	if s == "" {
		return 0, errors.New("empty")
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errors.New("not a number")
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

// Package oui looks up a network card's manufacturer from its MAC address, using
// Wireshark's "manuf" list (fetched while building the image).
package oui

import (
	"bufio"
	"os"
	"strings"
	"sync"
)

// Path is where the image keeps the list.
var Path = "/usr/local/share/homewarden/manuf"

var (
	once  sync.Once
	table map[string]string
)

func load() {
	table = map[string]string{}
	f, err := os.Open(Path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 2 || strings.Contains(parts[0], "/") {
			continue // only plain 24-bit prefixes
		}
		name := parts[1]
		if len(parts) >= 3 && parts[2] != "" {
			name = parts[2]
		}
		table[normalize(parts[0])] = name
	}
}

func normalize(mac string) string {
	m := strings.ToUpper(strings.NewReplacer(":", "", "-", "", ".", "").Replace(strings.TrimSpace(mac)))
	if len(m) < 6 {
		return ""
	}
	return m[:6]
}

// Lookup returns the vendor of a MAC address, or "" when unknown. Locally
// administered addresses (randomised phone MACs, containers) have no vendor.
func Lookup(mac string) string {
	p := normalize(mac)
	if p == "" {
		return ""
	}
	if b := hexNibble(p[1]); b >= 0 && b&2 != 0 {
		return ""
	}
	once.Do(load)
	return table[p]
}

func hexNibble(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// SetTable replaces the table (tests).
func SetTable(t map[string]string) {
	once.Do(func() {})
	table = t
}

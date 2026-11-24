package conn

import "testing"

func TestSudo(t *testing.T) {
	ok := []string{
		"apt-get update -qq",
		"DEBIAN_FRONTEND=noninteractive LANG=C apt-get -y -o Dpkg::Options::=--force-confdef dist-upgrade",
		"smartctl --scan 2>/dev/null",
		"apt-get -s upgrade 2>&1",
		"cmd &>/dev/null",
		"cmd >|out",
		`pct exec 101 -- sh -c "command -v apt-get >/dev/null && LANG=C apt-get -s dist-upgrade"`,
		`sh -c 'a; b | c'`,
		`echo a\;b \&\& c`,
		`echo "it's; fine"`,
	}
	for _, c := range ok {
		got, err := Sudo(c)
		if err != nil {
			t.Errorf("Sudo(%q): %v", c, err)
		} else if got != "sudo -n "+c {
			t.Errorf("Sudo(%q) = %q", c, got)
		}
	}
	bad := map[string]string{
		"apt-get update && apt-get -y upgrade": "&&",
		"apt-get update || true":               "||",
		"apt-get update; reboot":               ";",
		"dpkg -l | grep linux":                 "|",
		"sleep 5 & reboot":                     "&",
		"apt-get update\nreboot":               "newline",
		"apt-get install $(cat /tmp/list)":     "$(",
		"apt-get install `cat /tmp/list`":      "`",
		`sh -c "x $(id -u)"`:                   "$(",
		`echo 'quoted' && reboot`:              "&&",
	}
	for c, op := range bad {
		if _, err := Sudo(c); err == nil {
			t.Errorf("Sudo(%q) accepted a compound command", c)
		} else if got := compoundOperator(c); got != op {
			t.Errorf("compoundOperator(%q) = %q, want %q", c, got, op)
		}
	}
}

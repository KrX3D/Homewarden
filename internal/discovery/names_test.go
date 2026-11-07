package discovery

import "testing"

func TestLooksGenerated(t *testing.T) {
	for _, n := range []string{"", "8399d7f4f700", "8399d7f4f700.lan", "192.168.90.81"} {
		if !LooksGenerated(n) {
			t.Errorf("%q should look generated", n)
		}
	}
	for _, n := range []string{"octopi.lan", "Unraid", "abc"} {
		if LooksGenerated(n) {
			t.Errorf("%q is a real name", n)
		}
	}
}

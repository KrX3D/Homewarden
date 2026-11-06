package oui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLookup(t *testing.T) {
	dir := t.TempDir()
	Path = filepath.Join(dir, "manuf")
	os.WriteFile(Path, []byte("# comment\nB8:27:EB\tRaspberr\tRaspberry Pi Foundation\n00:1B:63\tApple\n00:50:C2:00:00:00/36\tIEEE\tsomething\n"), 0o644)
	cases := map[string]string{
		"b8:27:eb:12:34:56": "Raspberry Pi Foundation",
		"00-1B-63-aa-bb-cc": "Apple",
		"da:27:eb:00:00:01": "", // locally administered (randomised)
		"":                  "",
	}
	for mac, want := range cases {
		if got := Lookup(mac); got != want {
			t.Errorf("%q: got %q want %q", mac, got, want)
		}
	}
}

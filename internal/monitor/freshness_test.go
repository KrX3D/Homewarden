package monitor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBackupResult(t *testing.T) {
	c := Config{Path: "/mnt/user/backups/unifi", MaxAgeHours: 26}
	cases := []struct {
		out  string
		ok   bool
		want string
	}{
		{"1800000000\n3\n1799990000 /mnt/user/backups/unifi/a.unf\n", true, "a.unf, 3 files"},
		{"1800000000\n3\n1790000000 /mnt/user/backups/unifi/old.unf\n", false, "newest file 115 d old, max 26 h"},
		{"1800000000\nnopath\n", false, "not found"},
		{"1800000000\n0\n\n", false, "no files in"},
		{"garbage", false, "unexpected output"},
	}
	for _, tc := range cases {
		r := BackupResult(tc.out, c)
		if r.OK != tc.ok || !strings.Contains(r.Msg, tc.want) {
			t.Errorf("%q: got %v %q", tc.out, r.OK, r.Msg)
		}
	}
}

func TestParityResult(t *testing.T) {
	// now = 2026-11-13 12:00 +0100
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.FixedZone("", 3600)).Unix()
	head := func(extra string) string {
		return strings.Join([]string{strconv.FormatInt(now, 10) + " +0100 2026", extra, "---"}, "\n") + "\n"
	}
	ok := "2026 Nov  1 05:32:10|19930|200.7 MB/s|0|0|check P|3907018532\n"
	cases := []struct {
		name, out string
		max       int
		ok        bool
		want      string
	}{
		{"fresh", head("") + "2026 May  1 05:00:00|1|1|0|0|check P|1\n" + ok, 0, true, "12 d ago (2026-11-01), 0 errors"},
		{"stale", head("") + "2026 May 31 05:00:00|1|1|0|0|check P|1\n2026 Oct 15 01:00:00|1|1|-4|0|check P|1\n", 0, false, "(2026-05-31), max 35 d, 1 cancelled since"},
		{"errors", head("") + "2026 Nov  1 05:32:10|1|1|0|12|check P|1\n", 0, false, "found 12 errors"},
		{"sync ignored", head("") + "2026 Nov  1 05:32:10|1|1|0|0|recon P|1\n", 0, false, "no finished parity check"},
		{"old format", head("") + "Nov  1 05:32:10|19930|200.7 MB/s|0|0\n", 0, true, "2026-11-01"},
		{"max", head("") + ok, 24 * 7, false, "max 7 d"},
		{"running", head("mdResyncPos=\"500\"\nmdResyncSize=\"1000\"\nmdResyncAction=\"check P\"") + ok, 1, true, "running: check P (50%)"},
		{"empty log", head(""), 0, false, "no finished parity check"},
	}
	for _, tc := range cases {
		r := ParityResult(tc.out, Config{MaxAgeHours: tc.max})
		if r.OK != tc.ok || !strings.Contains(r.Msg, tc.want) {
			t.Errorf("%s: got %v %q", tc.name, r.OK, r.Msg)
		}
	}
}

// TestBackupScript runs the generated shell script locally (needs sh, find, stat, awk).
func TestBackupScript(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	sub := filepath.Join(dir, "it's here")
	os.MkdirAll(sub, 0o755)
	old := filepath.Join(dir, "old.unf")
	os.WriteFile(old, []byte("x"), 0o644)
	os.Chtimes(old, time.Now().Add(-72*time.Hour), time.Now().Add(-72*time.Hour))
	os.WriteFile(filepath.Join(sub, "new.unf"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(sub, "notes.txt"), []byte("x"), 0o644)
	run := func(ctx context.Context, cmd string) (string, error) {
		out, err := exec.CommandContext(ctx, "sh", "-c", cmd).Output()
		return string(out), err
	}
	r := checkFreshness(context.Background(), "backup", Config{Path: dir, Pattern: "*.unf"}, run)
	if !r.OK || !strings.Contains(r.Msg, "new.unf, 2 files") {
		t.Fatalf("got %v %q", r.OK, r.Msg)
	}
	r = checkFreshness(context.Background(), "backup", Config{Path: dir, Pattern: "old*", MaxAgeHours: 24}, run)
	if r.OK || !strings.Contains(r.Msg, "old.unf") {
		t.Fatalf("got %v %q", r.OK, r.Msg)
	}
	r = checkFreshness(context.Background(), "backup", Config{Path: filepath.Join(dir, "missing")}, run)
	if r.OK || !strings.Contains(r.Msg, "not found") {
		t.Fatalf("got %v %q", r.OK, r.Msg)
	}
}

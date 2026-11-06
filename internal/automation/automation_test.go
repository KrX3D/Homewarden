package automation

import (
	"testing"
	"time"
)

func TestDue(t *testing.T) {
	at := time.Date(2026, 11, 8, 3, 0, 0, 0, time.Local) // a Sunday
	if !Due("0 3 * * 0", at) || !Due("0 3 * * *", at.Add(20*time.Second)) {
		t.Fatal("should be due at 03:00")
	}
	if Due("0 3 * * *", at.Add(time.Minute)) || Due("0 3 * * 1", at) {
		t.Fatal("must not be due")
	}
	if ValidCron("every sunday") || !ValidCron("@daily") {
		t.Fatal("ValidCron")
	}
}

func TestSlug(t *testing.T) {
	if Slug("OctoPi Left") != "octopi_left" || Slug("  Unraid (main) ") != "unraid_main" {
		t.Fatal(Slug("OctoPi Left"))
	}
}

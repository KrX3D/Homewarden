package monitor

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"
)

// Freshness checks run a small shell script on the monitor's device (SSH or agent,
// as the connection user, read only):
//   - backup: newest file below a folder (optionally matching a name pattern) must be
//     younger than maxAgeHours.
//   - parity: Unraid's last finished parity check (/boot/config/parity-checks.log)
//     must be younger than maxAgeHours, ended normally and found no errors. A check
//     that is running right now counts as fresh.
// Ages are measured with the device's own clock, so clock skew does not matter.

// DefaultMaxAge is the max age in hours when none is set.
var DefaultMaxAge = map[string]int{"backup": 26, "parity": 35 * 24}

// RemoteType reports types that run on the monitor's device.
func RemoteType(typ string) bool { return typ == "backup" || typ == "parity" }

func (c Config) maxAge(typ string) time.Duration {
	h := c.MaxAgeHours
	if h <= 0 {
		h = DefaultMaxAge[typ]
	}
	return time.Duration(h) * time.Hour
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// FreshnessScript is the command run on the device.
func FreshnessScript(typ string, c Config) (string, error) {
	switch typ {
	case "backup":
		p := strings.TrimSpace(c.Path)
		if p == "" {
			return "", errors.New("no folder set")
		}
		name := ""
		if c.Pattern != "" {
			name = " -name " + shellQuote(c.Pattern)
		}
		// Line 1: now; then "nopath", or the file count and "<mtime> <path>" of the newest.
		return "P=" + shellQuote(p) + `; date +%s; [ -e "$P" ] || { echo nopath; exit 0; }; ` +
			`find "$P" -type f` + name + ` -exec stat -c '%Y %n' {} + 2>/dev/null | ` +
			`awk '{n++; if ($1+0 > m) {m=$1+0; l=$0}} END {print n+0; print l}'`, nil
	case "parity":
		return `date '+%s %z %Y'; grep -s '^mdResync' /var/local/emhttp/var.ini; echo ---; tail -n 100 /boot/config/parity-checks.log 2>/dev/null`, nil
	}
	return "", errors.New("unknown type " + typ)
}

// Age formats a duration as "5 min", "3 h", "12 d".
func Age(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 72*time.Hour:
		return fmt.Sprintf("%d h", int(d.Hours()))
	}
	return fmt.Sprintf("%d d", int(d.Hours()/24))
}

// BackupResult reads FreshnessScript("backup") output.
func BackupResult(out string, c Config) Result {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	now, err := strconv.ParseInt(strings.TrimSpace(lines[0]), 10, 64)
	if err != nil || len(lines) < 2 {
		return Result{Msg: "unexpected output: " + short(out)}
	}
	if strings.TrimSpace(lines[1]) == "nopath" {
		return Result{Msg: "not found: " + c.Path}
	}
	count, _ := strconv.Atoi(strings.TrimSpace(lines[1]))
	if count == 0 || len(lines) < 3 {
		if c.Pattern != "" {
			return Result{Msg: fmt.Sprintf("no files matching %s in %s", c.Pattern, c.Path)}
		}
		return Result{Msg: "no files in " + c.Path}
	}
	ts, file, _ := strings.Cut(strings.TrimSpace(lines[2]), " ")
	mtime, err := strconv.ParseFloat(ts, 64)
	if err != nil {
		return Result{Msg: "unexpected output: " + short(out)}
	}
	age := time.Duration(now-int64(mtime)) * time.Second
	if age < 0 {
		age = 0
	}
	max := c.maxAge("backup")
	when := time.Unix(int64(mtime), 0).Format("2006-01-02 15:04")
	msg := fmt.Sprintf("newest %s old (%s): %s, %d files", Age(age), when, path.Base(file), count)
	if age > max {
		return Result{Msg: fmt.Sprintf("newest file %s old, max %s (%s): %s", Age(age), Age(max), when, path.Base(file))}
	}
	return Result{OK: true, Msg: msg}
}

// parityRow is one line of parity-checks.log:
// date|duration s|speed|exit code|errors[|action|size]. Exit 0 = finished, -4 = cancelled.
type parityRow struct {
	end    time.Time
	exit   int
	errors int64
	action string
}

func parseParityRow(line string, loc *time.Location, year int) (parityRow, bool) {
	f := strings.Split(line, "|")
	if len(f) < 5 {
		return parityRow{}, false
	}
	date := strings.Join(strings.Fields(f[0]), " ")
	t, err := time.ParseInLocation("2006 Jan 2 15:04:05", date, loc)
	if err != nil {
		// Old Unraid versions wrote no year.
		if t, err = time.ParseInLocation("2006 Jan 2 15:04:05", strconv.Itoa(year)+" "+date, loc); err != nil {
			return parityRow{}, false
		}
	}
	exit, err1 := strconv.Atoi(strings.TrimSpace(f[3]))
	errs, err2 := strconv.ParseInt(strings.TrimSpace(f[4]), 10, 64)
	if err1 != nil || err2 != nil {
		return parityRow{}, false
	}
	r := parityRow{end: t, exit: exit, errors: errs}
	if len(f) > 5 {
		r.action = strings.TrimSpace(f[5])
	}
	return r, true
}

// ParityResult reads FreshnessScript("parity") output.
func ParityResult(out string, c Config) Result {
	head, log, _ := strings.Cut(out, "---")
	hl := strings.Split(strings.TrimSpace(head), "\n")
	f := strings.Fields(hl[0])
	if len(f) < 3 {
		return Result{Msg: "unexpected output: " + short(out)}
	}
	nowUnix, err := strconv.ParseInt(f[0], 10, 64)
	if err != nil {
		return Result{Msg: "unexpected output: " + short(out)}
	}
	now := time.Unix(nowUnix, 0)
	loc := time.UTC
	if z, err := time.Parse("-0700", f[1]); err == nil {
		_, off := z.Zone()
		loc = time.FixedZone(f[1], off)
	}
	year, _ := strconv.Atoi(f[2])

	// A check running right now (mdResyncPos > 0).
	vars := map[string]string{}
	for _, l := range hl[1:] {
		if k, v, ok := strings.Cut(l, "="); ok {
			vars[k] = strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	if pos, _ := strconv.ParseInt(vars["mdResyncPos"], 10, 64); pos > 0 {
		msg := "running"
		if a := vars["mdResyncAction"]; a != "" {
			msg += ": " + a
		}
		if size, _ := strconv.ParseInt(vars["mdResyncSize"], 10, 64); size > 0 {
			msg += fmt.Sprintf(" (%d%%)", pos*100/size)
		}
		return Result{OK: true, Msg: msg}
	}

	var last *parityRow
	cancelled := 0
	for _, l := range strings.Split(strings.TrimSpace(log), "\n") {
		r, ok := parseParityRow(l, loc, year)
		if !ok || r.action != "" && !strings.HasPrefix(strings.ToLower(r.action), "check") {
			continue // parity sync, disk clear, rebuild
		}
		if r.exit != 0 {
			cancelled++
			continue
		}
		rr := r // the log is in order: the newest finished check wins
		last, cancelled = &rr, 0
	}
	if last == nil {
		return Result{Msg: "no finished parity check in the log"}
	}
	age := now.Sub(last.end)
	if age < 0 {
		age = 0
	}
	max := c.maxAge("parity")
	when := last.end.Format("2006-01-02")
	note := ""
	if cancelled > 0 {
		note = fmt.Sprintf(", %d cancelled since", cancelled)
	}
	switch {
	case age > max:
		return Result{Msg: fmt.Sprintf("last parity check %s ago (%s), max %s%s", Age(age), when, Age(max), note)}
	case last.errors > 0:
		return Result{Msg: fmt.Sprintf("last parity check (%s) found %d errors", when, last.errors)}
	}
	return Result{OK: true, Msg: fmt.Sprintf("last parity check %s ago (%s), 0 errors%s", Age(age), when, note)}
}

// checkFreshness runs the script through exec and reads the result.
func checkFreshness(ctx context.Context, typ string, c Config, exec func(ctx context.Context, cmd string) (string, error)) Result {
	start := time.Now()
	script, err := FreshnessScript(typ, c)
	if err != nil {
		return Result{Msg: err.Error()}
	}
	out, err := exec(ctx, script)
	if err != nil {
		return Result{Msg: err.Error()}
	}
	var r Result
	if typ == "backup" {
		r = BackupResult(out, c)
	} else {
		r = ParityResult(out, c)
	}
	r.Ms = float64(time.Since(start).Microseconds()) / 1000
	return r
}

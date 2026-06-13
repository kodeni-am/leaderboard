// Package window is the SP4 time-window lifecycle: resolving the current window
// id for a cadence, and reaping stale time-bucketed boards from the Redis cache
// once they age out (the durable log remains the source of truth, so expired
// boards can always be rebuilt).
package window

import (
	"strconv"
	"strings"
	"time"

	"github.com/araasr/leaderboard/pkg/engine"
)

// Resolve maps a window query parameter to a concrete window id. A cadence
// keyword ("daily","weekly","monthly","alltime") resolves against now; anything
// else is treated as a literal window id (e.g. "d=2026-06-13", "s=spring2026").
func Resolve(param string, now time.Time) string {
	switch strings.ToLower(param) {
	case "", "all", "alltime":
		return "all"
	case "daily":
		return (engine.WindowSpec{Kind: engine.WindowDaily}).WindowID(now)
	case "weekly":
		return (engine.WindowSpec{Kind: engine.WindowWeekly}).WindowID(now)
	case "monthly":
		return (engine.WindowSpec{Kind: engine.WindowMonthly}).WindowID(now)
	default:
		return param
	}
}

// ParseEnd returns the instant a dated window ends (exclusive). ok is false for
// non-dated windows ("all", custom/seasonal ids) which never expire.
func ParseEnd(id string) (time.Time, bool) {
	switch {
	case strings.HasPrefix(id, "d="):
		d, err := time.Parse("2006-01-02", id[2:])
		if err != nil {
			return time.Time{}, false
		}
		return d.AddDate(0, 0, 1), true
	case strings.HasPrefix(id, "m="):
		m, err := time.Parse("2006-01", id[2:])
		if err != nil {
			return time.Time{}, false
		}
		return m.AddDate(0, 1, 0), true
	case strings.HasPrefix(id, "w="):
		// format w=YYYY-Www
		body := id[2:]
		parts := strings.SplitN(body, "-W", 2)
		if len(parts) != 2 {
			return time.Time{}, false
		}
		year, err1 := strconv.Atoi(parts[0])
		week, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil {
			return time.Time{}, false
		}
		return isoWeekStart(year, week).AddDate(0, 0, 7), true
	default:
		return time.Time{}, false
	}
}

// isoWeekStart returns the Monday 00:00 UTC that begins the given ISO week.
func isoWeekStart(year, week int) time.Time {
	// ISO 8601: week 1 is the week containing January 4th.
	jan4 := time.Date(year, 1, 4, 0, 0, 0, 0, time.UTC)
	weekday := (int(jan4.Weekday()) + 6) % 7 // Monday=0 .. Sunday=6
	week1Monday := jan4.AddDate(0, 0, -weekday)
	return week1Monday.AddDate(0, 0, (week-1)*7)
}

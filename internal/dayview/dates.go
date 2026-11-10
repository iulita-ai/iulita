package dayview

import (
	"strings"
	"time"
)

// DueToday includes overdue, today's and unscheduled work. Future work is
// excluded. Date-only provider dates are calendar values, never converted UTC instants.
func DueToday(raw string, now time.Time, loc *time.Location) bool {
	if loc == nil {
		loc = time.UTC
	}
	if raw == "" {
		return true
	}
	today := now.In(loc).Format("2006-01-02")
	if len(raw) >= 10 {
		date := raw[:10]
		if _, err := time.Parse("2006-01-02", date); err == nil {
			return strings.Compare(date, today) <= 0
		}
	}
	return false
}

// ItemDueToday respects actual timestamps for sources that preserve time.
func ItemDueToday(i Item, now time.Time, loc *time.Location) bool {
	if loc == nil {
		loc = time.UTC
	}
	if i.Due != "" && !i.DateOnly {
		if t, e := time.Parse(time.RFC3339, i.Due); e == nil {
			return t.In(loc).Format("2006-01-02") <= now.In(loc).Format("2006-01-02")
		}
	}
	return DueToday(i.Due, now, loc)
}

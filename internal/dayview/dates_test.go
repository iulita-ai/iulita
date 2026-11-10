package dayview

import (
	"testing"
	"time"
)

func TestDueTodayUsesCalendarDayAcrossTimezones(t *testing.T) {
	loc, _ := time.LoadLocation("Pacific/Honolulu")
	now := time.Date(2026, 11, 10, 1, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		due  string
		want bool
	}{{"", true}, {"2026-11-08", true}, {"2026-11-09T00:00:00Z", true}, {"2026-11-10T00:00:00Z", false}, {"invalid", false}} {
		if DueToday(tc.due, now, loc) != tc.want {
			t.Fatal(tc)
		}
	}
}

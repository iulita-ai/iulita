package conversations

import (
	"testing"
	"time"
)

func TestReminderMissingTimezoneUsesIrkutsk(t *testing.T) {
	for _, tc := range []struct{ due, zone, utc string }{
		{"2030-01-02T10:00:00", "", "2030-01-02T02:00:00Z"},
		{"2030-01-02T10:00:00", "UTC", "2030-01-02T10:00:00Z"},
		{"2030-01-02T10:00:00", "Europe/Moscow", "2030-01-02T07:00:00Z"},
		{"2030-01-02T10:00:00Z", "", "2030-01-02T10:00:00Z"},
		{"2030-01-02T10:00:00+03:00", "", "2030-01-02T07:00:00Z"},
	} {
		got, err := ReminderTime(ReminderInput{DueAt: tc.due, Timezone: tc.zone})
		if err != nil || got.Format(time.RFC3339) != tc.utc {
			t.Fatalf("%+v: %s, %v", tc, got, err)
		}
	}
	if _, err := ReminderTime(ReminderInput{DueAt: "2030-01-02T10:00:00", Timezone: "Unknown/Zone"}); err == nil {
		t.Fatal("invalid explicit timezone silently replaced")
	}
}

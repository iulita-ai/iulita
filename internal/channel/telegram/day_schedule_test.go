package telegram

import (
	"context"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/dayview"
)

func TestDaySchedulePresetsManualAndDST(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 3, 28, 13, 0, 0, 0, loc)
	due, err := parseDaySchedule("tomorrow09", "", now, loc)
	if err != nil || due.Hour() != 9 || due.Day() != 29 {
		t.Fatalf("tomorrow: %v %v", due, err)
	}
	_, offset := due.Zone()
	if offset != 7200 {
		t.Fatal("DST offset ignored")
	}
	due, err = parseDaySchedule("plusday", "2026-03-28T15:00:00+01:00", now, loc)
	if err != nil || due.Hour() != 15 || due.Day() != 29 {
		t.Fatalf("plusday: %v %v", due, err)
	}
	due, err = parseDaySchedule("2026-10-25T02:30:00+01:00", "", now, loc)
	if err != nil {
		t.Fatal(err)
	}
	_, offset = due.Zone()
	if offset != 3600 {
		t.Fatal("explicit offset ambiguity lost")
	}
	for _, input := range []string{"2026-03-29T02:30:00", "2026-03-28T01:00:00+01:00", "not a time", "2026-03-29"} {
		if _, err = parseDaySchedule(input, "", now, loc); err == nil {
			t.Fatalf("invalid manual input accepted: %q", input)
		}
	}
	if _, err = parseDaySchedule("plusday", "", now, loc); err == nil {
		t.Fatal("missing original due accepted")
	}
}
func TestDayScheduleRefusesNonPrivateOrUnboundCard(t *testing.T) {
	c := &Channel{instanceID: "bot"}
	for _, card := range []*dayview.Card{nil, {Owner: "owner", Chat: -1, Instance: "bot", Expires: time.Now().Add(time.Minute)}, {Owner: "owner", Chat: 7, Instance: "other", Expires: time.Now().Add(time.Minute)}, {Chat: 7, Instance: "bot", Expires: time.Now().Add(time.Minute)}} {
		if _, err := c.pickDaySchedule(context.Background(), card, dayview.Item{}, "remind", "UTC"); err == nil {
			t.Fatal("unbound principal accepted")
		}
	}
}

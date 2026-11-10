package telegram

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/iulita-ai/iulita/internal/dayview"
)

func TestDayProjectionByteBudgetLocalesAndSourceStates(t *testing.T) {
	c := &dayview.Card{ID: strings.Repeat("a", 16), Revision: 9223372036854775807}
	for _, locale := range []string{"en", "ru"} {
		data := cardData{Locale: locale, Actions: true, Zone: "Europe/Moscow", Sections: []dayview.Section{{Name: strings.Repeat("😀", 50), Status: "ready", Items: []dayview.Item{{Source: "todoist", Title: strings.Repeat("Кириллица😀", 100), Due: strings.Repeat("дата", 25), Complete: true, Reschedule: true}}}, {Name: "Calendar", Status: "unavailable"}, {Name: "Tasks", Status: "not_connected"}}}
		raw, err := dayProjection(c, data)
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) > 1024 || !utf8.ValidString(raw) {
			t.Fatal("budget/encoding failure")
		}
		var state dayState
		if err = json.Unmarshal([]byte(raw), &state); err != nil {
			t.Fatal(err)
		}
		if state.Lang != locale || len(state.Rows) != 1 {
			t.Fatalf("invalid state %+v", state)
		}
		if locale == "en" && strings.Contains(raw, "Мой день") {
			t.Fatal("wrong language")
		}
	}
	if err := validateDayHTML(); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"ready", "unavailable", "not_connected"} {
		raw, err := dayProjection(c, cardData{Locale: "en", Zone: "UTC", Sections: []dayview.Section{{Name: "tasks", Status: status}}})
		if err != nil {
			t.Fatal(err)
		}
		if status == "ready" && !strings.Contains(raw, "No items") {
			t.Fatal("empty state absent")
		}
		if status != "ready" && strings.Contains(raw, "No items") {
			t.Fatal("source failure rendered as empty")
		}
	}
}

func TestDayProjectionActionsAndUnknownOutcome(t *testing.T) {
	c := &dayview.Card{ID: "card", Revision: 1}
	data := cardData{Locale: "en", Zone: "UTC", Sections: []dayview.Section{{Name: "tasks", Status: "ready", Items: []dayview.Item{{Title: "task", Complete: true, Reschedule: true}}}}}
	for _, mode := range []string{"off", "on", "unknown"} {
		data.Actions = mode != "off"
		data.OperationStatus = ""
		if mode == "unknown" {
			data.OperationStatus = "unknown_outcome"
		}
		raw, err := dayProjection(c, data)
		if err != nil {
			t.Fatal(err)
		}
		var state dayState
		_ = json.Unmarshal([]byte(raw), &state)
		if (state.Rows[0].Complete != "") != (mode == "on") {
			t.Fatal("action controls do not match policy")
		}
		if mode == "unknown" && !strings.Contains(state.Status, "Do not repeat") {
			t.Fatal("unknown result hidden")
		}
	}
	p := dayActionPreview("en", dayview.Item{Title: "Task", Source: "Todoist", Due: "2026-11-10"}, "complete", time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC), "UTC")
	if strings.Contains(p, "2026-11-11") || !strings.Contains(p, "Todoist") {
		t.Fatal("misleading complete preview")
	}
	p = dayActionPreview("ru", dayview.Item{Title: "Task", Source: "Todoist", Due: "2026-11-10"}, "reschedule", time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC), "UTC")
	if !strings.Contains(p, "2026-11-10 → 2026-11-11") || !strings.Contains(p, "Перенести") {
		t.Fatal("reschedule old/new/locale missing")
	}
}

func TestDayCategoryNavigationPreservesGlobalActionIndices(t *testing.T) {
	card := &dayview.Card{ID: strings.Repeat("a", 16), Revision: 9223372036854775807}
	data := cardData{Locale: "ru", Actions: true, Zone: "Europe/Moscow", Sections: []dayview.Section{{Name: "todoist", Status: "ready", Items: []dayview.Item{{Title: "Task", Source: "todoist", Complete: true}}}, {Name: "reminders", Status: "ready", Items: []dayview.Item{{Title: "Reminder", Source: "reminders", Reschedule: true}}}, {Name: "calendar", Status: "ready", Items: []dayview.Item{{Title: "Event", Source: "calendar"}}}, {Name: "jobs", Status: "ready", Items: []dayview.Item{{Title: "Job", Source: "jobs"}}}}}
	for section := 0; section < 4; section++ {
		data.Section = section
		raw, err := dayProjection(card, data)
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) > 1024 {
			t.Fatal("byte budget")
		}
		var state dayState
		_ = json.Unmarshal([]byte(raw), &state)
		if state.Category != dayCategoryLabel("ru", section) || len(state.Rows) != 1 || state.Switch != dayAction(card, "section", (section+1)%4) {
			t.Fatalf("section not projected: %+v", state)
		}
		if section == 1 && state.Rows[0].Snooze != dayAction(card, "reschedule", 1) {
			t.Fatal("filtered index points at another source")
		}
	}
}

func TestDayActionHandlesFitTelegramLimit(t *testing.T) {
	card := &dayview.Card{ID: strings.Repeat("a", 32), Revision: 9223372036854775807}
	for _, verb := range []string{"acknowledge", "reschedule", "complete", "remind", "refresh", "details", "publish", "section", "result"} {
		if handle := dayAction(card, verb, 1000); len(handle) > 64 {
			t.Fatalf("%s too long: %d", verb, len(handle))
		}
	}
}

func TestCraftDeadlineShownWithoutChangingRescheduleMeaning(t *testing.T) {
	item := dayview.Item{Source: "craft_tasks", Title: "Task", Due: "2026-12-01", Deadline: "2026-11-01", DateOnly: true}
	if got := dayDueSummary(item, "ru"); got != "Срок: 2026-11-01" {
		t.Fatal(got)
	}
	if got := dayDueSummary(item, "en"); got != "Deadline: 2026-11-01" {
		t.Fatal(got)
	}
	target := time.Date(2026, 12, 2, 9, 0, 0, 0, time.UTC)
	preview := dayActionPreview("en", item, "reschedule", target, "UTC")
	if !strings.Contains(preview, "New scheduled date") || !strings.Contains(preview, "Deadline remains unchanged: 2026-11-01") || !strings.Contains(preview, "time is not saved") {
		t.Fatal(preview)
	}
	item.Due = item.Deadline
	if got := dayDueSummary(item, "ru"); got != item.Due {
		t.Fatal(got)
	}
}

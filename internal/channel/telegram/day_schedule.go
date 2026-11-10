package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/iulita-ai/iulita/internal/dayview"
	"github.com/iulita-ai/iulita/internal/i18n"
	"github.com/iulita-ai/iulita/internal/security"
	"github.com/iulita-ai/iulita/internal/skill/interact"
)

// pickDaySchedule uses the immutable private card principal, never last-sender
// state. The resulting instant is still subject to the exact-operation approval.
func (c *Channel) pickDaySchedule(ctx context.Context, card *dayview.Card, item dayview.Item, verb, zone string) (time.Time, error) {
	if card == nil || card.Chat <= 0 || card.Owner == "" || card.Instance != c.instanceID || !time.Now().Before(card.Expires) || (verb != "remind" && verb != "reschedule") {
		return time.Time{}, fmt.Errorf("private schedule actor unavailable")
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid schedule timezone")
	}
	var data cardData
	if err = json.Unmarshal([]byte(card.ItemsJSON), &data); err != nil {
		return time.Time{}, fmt.Errorf("invalid schedule card")
	}
	prompt := &telegramPrompter{channel: c, chatID: card.Chat, threadID: card.Thread, actor: card.Chat, prompts: c.prompts}
	ctx = i18n.WithLocale(ctx, i18n.ResolveLocale(data.Locale, "en"))
	for attempt := 0; attempt < 3; attempt++ {
		now := time.Now().In(loc)
		next := now.AddDate(0, 0, 1)
		tomorrow := time.Date(next.Year(), next.Month(), next.Day(), 9, 0, 0, 0, loc)
		options := []interact.Option{{ID: "tomorrow09", Label: dayText(data.Locale, "Tomorrow at 09:00", "Завтра в 09:00") + " · " + tomorrow.Format("02.01")}}
		if old, ok := dayOriginalDue(item.Due, loc); ok && old.AddDate(0, 0, 1).After(now) {
			options = append(options, interact.Option{ID: "plusday", Label: dayText(data.Locale, "Current due +1 day", "Текущий срок +1 день") + " · " + old.AddDate(0, 0, 1).Format("02.01 15:04")})
		}
		question := fmt.Sprintf("%s\n%s\n%s: %s\n%s", dayText(data.Locale, "Choose date and time", "Выберите дату и время"), truncateRunes(security.RedactSensitiveText(item.Title), 100), dayText(data.Locale, "Timezone", "Часовой пояс"), zone, dayText(data.Locale, "For another time choose Enter manually and send RFC3339 with an explicit offset, e.g. 2026-12-01T18:30:00+03:00.", "Для другого времени выберите «Ввести вручную» и отправьте RFC3339 с явным смещением, например 2026-12-01T18:30:00+03:00."))
		if attempt > 0 {
			question = dayText(data.Locale, "Invalid or past time. Use RFC3339 with an explicit offset.", "Некорректное или прошедшее время. Используйте RFC3339 с явным смещением.") + "\n\n" + question
		}
		answer, e := prompt.Ask(ctx, question, options)
		if e != nil {
			return time.Time{}, e
		}
		due, e := parseDaySchedule(answer, item.Due, time.Now(), loc)
		if e == nil {
			return due, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid schedule input")
}

func dayOriginalDue(raw string, loc *time.Location) (time.Time, bool) {
	if t, e := time.Parse(time.RFC3339, raw); e == nil {
		return t.In(loc), true
	}
	if t, e := time.ParseInLocation("2006-01-02", raw, loc); e == nil {
		return t, true
	}
	return time.Time{}, false
}
func parseDaySchedule(answer, old string, now time.Time, loc *time.Location) (time.Time, error) {
	if loc == nil {
		return time.Time{}, fmt.Errorf("schedule timezone required")
	}
	now = now.In(loc)
	var due time.Time
	switch answer {
	case "tomorrow09":
		n := now.AddDate(0, 0, 1)
		due = time.Date(n.Year(), n.Month(), n.Day(), 9, 0, 0, 0, loc)
	case "plusday":
		original, ok := dayOriginalDue(old, loc)
		if !ok {
			return time.Time{}, fmt.Errorf("original due unavailable")
		}
		due = original.AddDate(0, 0, 1)
	default:
		t, e := time.Parse(time.RFC3339, strings.TrimSpace(answer))
		if e != nil {
			return time.Time{}, fmt.Errorf("explicit RFC3339 offset required")
		}
		due = t.In(loc)
	}
	if !due.After(now) {
		return time.Time{}, fmt.Errorf("schedule must be in future")
	}
	return due, nil
}

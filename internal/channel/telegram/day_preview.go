package telegram

import (
	"fmt"
	"time"

	"github.com/iulita-ai/iulita/internal/dayview"
	"github.com/iulita-ai/iulita/internal/security"
)

// dayActionPreview names the exact effect before the identity-bound approval.
func dayActionPreview(locale string, item dayview.Item, verb string, due time.Time, zone string) string {
	title := truncateRunes(security.RedactSensitiveText(item.Title), 120)
	source := truncateRunes(security.RedactSensitiveText(item.Source), 40)
	if item.Origin != "" {
		source += " · " + truncateRunes(security.RedactSensitiveText(item.Origin), 160)
	}
	if verb == "remind" {
		return fmt.Sprintf("%s\n%s\n%s: %s\n%s (%s)", dayText(locale, "Create reminder", "Создать напоминание"), title, dayText(locale, "Source", "Источник"), source, due.Format(time.RFC3339), zone)
	}
	if verb == "acknowledge" {
		if item.Source == "operations" {
			return dayText(locale, "Accept uncertainty and allow a new request? This does not retry the action or prove success.\n", "Принять неопределённость и разрешить новый запрос? Это не повторяет действие и не доказывает успех.\n") + title
		}
		return dayText(locale, "Acknowledge this uncertain outcome without retrying or declaring success?\n", "Подтвердить неизвестный результат без повтора и без отметки об успехе?\n") + title
	}
	if verb == "complete" {
		return fmt.Sprintf("%s\n%s\n%s: %s", dayText(locale, "Complete task", "Завершить задачу"), title, dayText(locale, "Source", "Источник"), source)
	}
	old := item.Due
	if old == "" {
		old = dayText(locale, "not set", "не задано")
	}
	notice := ""
	if item.DateOnly {
		notice = dayText(locale, "\nThis source stores only the date; time is not saved.", "\nИсточник хранит только дату; время не сохраняется.")
	}
	if item.Source == "craft_tasks" {
		deadline := item.Deadline
		if deadline == "" {
			deadline = dayText(locale, "not set", "не задано")
		}
		return fmt.Sprintf("%s\n%s\n%s: %s\n%s: %s (%s)\n%s: %s", dayText(locale, "Reschedule", "Перенести"), title, dayText(locale, "Source", "Источник"), source, dayText(locale, "New scheduled date", "Новая запланированная дата"), due.Format(time.RFC3339), zone, dayText(locale, "Deadline remains unchanged", "Срок остаётся прежним"), truncateRunes(security.RedactSensitiveText(deadline), 50)) + notice
	}
	return fmt.Sprintf("%s\n%s\n%s: %s\n%s: %s → %s (%s)", dayText(locale, "Reschedule", "Перенести"), title, dayText(locale, "Source", "Источник"), source, dayText(locale, "Due", "Срок"), truncateRunes(security.RedactSensitiveText(old), 50), due.Format(time.RFC3339), zone) + notice
}

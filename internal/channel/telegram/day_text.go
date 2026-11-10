package telegram

import (
	"context"
	"encoding/json"

	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/iulita-ai/iulita/internal/dayview"
	"github.com/iulita-ai/iulita/internal/security"
)

func dayTextView(card *dayview.Card, data cardData) (string, models.InlineKeyboardMarkup, error) {
	raw, e := dayProjection(card, data)
	if e != nil {
		return "", models.InlineKeyboardMarkup{}, e
	}
	var state dayState
	if e := json.Unmarshal([]byte(raw), &state); e != nil {
		return "", models.InlineKeyboardMarkup{}, e
	}
	text := state.Header + "\n" + state.Category + "\n" + state.Status + "\n"
	var rows [][]models.InlineKeyboardButton
	add := func(label, handle string) {
		if handle != "" {
			rows = append(rows, []models.InlineKeyboardButton{{Text: label, CallbackData: handle}})
		}
	}
	for rowIndex := range state.Rows {
		r := &state.Rows[rowIndex]
		text += "\n" + r.Title
		if r.Due != "" {
			text += "\n" + r.Due
		}
		add(dayText(data.Locale, "Details", "Подробнее"), r.Details)
		add(dayText(data.Locale, "Complete", "Выполнить"), r.Complete)
		add(dayText(data.Locale, "Reschedule…", "Перенести…"), r.Snooze)
		add(dayText(data.Locale, "Create reminder…", "Создать напоминание…"), r.Remind)
		add(dayText(data.Locale, "Result file", "Файл результата"), r.Download)
		add(dayText(data.Locale, "Publish…", "Опубликовать…"), r.Publish)
		add(dayText(data.Locale, "Acknowledge uncertainty", "Подтвердить неизвестную доставку"), r.Resolve)
	}
	add(dayText(data.Locale, "Previous", "Назад"), state.Previous)
	add(dayText(data.Locale, "Next", "Далее"), state.Next)
	add(dayText(data.Locale, "Section: ", "Раздел: ")+state.Category, state.Switch)
	add(dayText(data.Locale, "Refresh", "Обновить"), state.Refresh)
	return text, models.InlineKeyboardMarkup{InlineKeyboard: rows}, nil
}
func (c *Channel) sendDayText(ctx context.Context, card *dayview.Card, data cardData) error {
	if _, e := c.resolveTarget(ctx, data.Conversation); e != nil {
		return e
	}
	data.TextMode = true
	raw, marshalErr := json.Marshal(data)
	if marshalErr != nil {
		return marshalErr
	}
	card.ItemsJSON = string(raw)
	text, keyboard, e := dayTextView(card, data)
	if e != nil {
		return e
	}
	msg, e := c.bot.SendMessage(ctx, &bot.SendMessageParams{ChatID: card.Chat, MessageThreadID: card.Thread, Text: text, ReplyMarkup: keyboard, ProtectContent: true})
	if e != nil {
		return e
	}
	card.Message = msg.ID
	card.DesiredState = "text"
	card.DeliveredRevision = card.Revision
	return c.day.Store.SaveDayCard(ctx, card)
}
func (c *Channel) handleDayCallback(ctx context.Context, cq *models.CallbackQuery) bool {
	if cq == nil {
		return false
	}
	if strings.HasPrefix(cq.Data, "dayback:") {
		if _, deliveryErr := c.bot.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: cq.ID}); deliveryErr != nil {
			c.noteFailure("Telegram AnswerCallbackQuery failed", deliveryErr)
		}
		if c.day == nil || cq.Message.Message == nil {
			return true
		}
		card, e := c.day.Store.GetDayCard(ctx, strings.TrimPrefix(cq.Data, "dayback:"))
		if e == nil && card != nil && card.Chat == cq.From.ID && card.Chat == cq.Message.Message.Chat.ID && card.Instance == c.instanceID {
			if _, deliveryErr := c.bot.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: card.Chat, MessageID: cq.Message.Message.ID}); deliveryErr != nil {
				c.noteFailure("Telegram DeleteMessage failed", deliveryErr)
			}
		}
		return true
	}
	if cq.Data == "myday:personal" {
		return c.handleDayPersonal(ctx, cq)
	}
	if !strings.HasPrefix(cq.Data, "d:") {
		return false
	}
	if cq.Message.Message == nil {
		return true
	}
	m := cq.Message.Message
	q := &models.WidgetQuery{ID: cq.ID, From: cq.From, Chat: &m.Chat, MessageID: m.ID, Data: cq.Data, Type: models.WidgetQueryType{Type: models.WidgetQueryTypeTypeData}}
	return c.dispatchDayQuery(ctx, q, func(text string) {
		if _, deliveryErr := c.bot.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: cq.ID, Text: truncateRunes(text, 150)}); deliveryErr != nil {
			c.noteFailure("Telegram AnswerCallbackQuery failed", deliveryErr)
		}
	})
}
func (c *Channel) handleDayPersonal(ctx context.Context, cq *models.CallbackQuery) bool {
	if _, deliveryErr := c.bot.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: cq.ID}); deliveryErr != nil {
		c.noteFailure("Telegram AnswerCallbackQuery failed", deliveryErr)
	}
	if c.day == nil || !c.isAllowed(cq.From.ID) || c.userResolver == nil {
		return true
	}
	// Resolve only the authenticated clicker's private route, never group memory.
	chat := cq.From.ID
	owner, e := c.dayOwner(ctx, cq.From, chat)
	if e != nil {
		return true
	}
	if !c.day.admit(owner) {
		return true
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	privateMessage := &models.Message{Chat: models.Chat{ID: chat, Type: models.ChatTypePrivate}, From: &cq.From}
	key, e := c.conversationFor(ctx, privateMessage)
	if e != nil || c.persistRoute(ctx, key, privateMessage, owner) != nil {
		return true
	}
	zone := "UTC"
	if c.store != nil {
		if user, userErr := c.store.GetUser(ctx, owner); userErr == nil && user != nil && user.Timezone != "" {
			zone = user.Timezone
		}
	}
	loc, e := time.LoadLocation(zone)
	if e != nil {
		loc = time.UTC
		zone = "UTC"
	}
	sections := c.day.Service(key).Read(ctx, owner, time.Now(), loc)
	sanitizeDaySections(sections)
	text := dayText(cq.From.LanguageCode, "Your day · ", "Ваш день · ") + zone
	for _, s := range sections {
		text += "\n\n" + s.Name
		if s.Status != "ready" {
			text += " · " + dayText(cq.From.LanguageCode, "unavailable", "недоступен")
			continue
		}
		for i := range s.Items {
			item := &s.Items[i]
			if i == 3 {
				break
			}
			text += "\n• " + truncateRunes(item.Title, 80)
		}
	}
	if deliveryErr := c.SendPrivateCallbackReply(ctx, cq, truncateRunes(security.RedactSensitiveText(text), 1800)); deliveryErr != nil {
		c.noteFailure("Telegram SendPrivateCallbackReply failed", deliveryErr)
	}
	return true
}
func (c *Channel) showDayDetail(ctx context.Context, card *dayview.Card, data cardData, index int, download bool) {
	var items []dayview.Item
	for _, s := range data.Sections {
		items = append(items, s.Items...)
	}
	if index < 0 || index >= len(items) {
		return
	}
	item := items[index]
	detail, e := c.day.Service(data.Conversation).Detail(ctx, card.Owner, item)
	if _, routeErr := c.resolveTarget(ctx, data.Conversation); routeErr != nil {
		return
	}
	if e != nil {
		c.sendSingleMessage(card.Chat, dayText(data.Locale, "Details unavailable. Refresh your day.", "Подробности недоступны. Обновите день."), 0, card.Thread)
		return
	}
	if download {
		if detail.Result == "" {
			c.sendSingleMessage(card.Chat, dayText(data.Locale, "No result file is available.", "Файл результата недоступен."), 0, card.Thread)
			return
		}
		safe := truncateRunes(security.RedactSensitiveText(detail.Result), 60000)
		if _, deliveryErr := c.bot.SendDocument(ctx, &bot.SendDocumentParams{ChatID: card.Chat, MessageThreadID: card.Thread, Document: &models.InputFileUpload{Filename: "job-result.txt", Data: strings.NewReader(safe)}, ProtectContent: true, Caption: dayText(data.Locale, "Job result", "Результат задания")}); deliveryErr != nil {
			c.noteFailure("Telegram SendDocument failed", deliveryErr)
		}
		return
	}
	loc, locationErr := time.LoadLocation(data.Zone)
	if locationErr != nil {
		loc = time.UTC
	}
	text := security.RedactSensitiveText(detail.Title) + "\n" + dayText(data.Locale, "Source: ", "Источник: ") + item.Source
	if detail.Status != "" {
		text += "\n" + localizedJobStatus(data.Locale, detail.Status)
	}
	if detail.StartedAt != nil {
		text += "\n" + dayText(data.Locale, "Started: ", "Начало: ") + detail.StartedAt.In(loc).Format(time.RFC3339)
	}
	if detail.FinishedAt != nil {
		text += "\n" + dayText(data.Locale, "Finished: ", "Окончание: ") + detail.FinishedAt.In(loc).Format(time.RFC3339)
		if detail.StartedAt != nil {
			text += "\n" + dayText(data.Locale, "Duration: ", "Длительность: ") + detail.FinishedAt.Sub(*detail.StartedAt).Round(time.Second).String()
		}
	}
	summary := detail.Summary
	if item.Source == "craft_tasks" && strings.HasPrefix(data.Locale, "ru") {
		summary = strings.ReplaceAll(strings.ReplaceAll(summary, "Schedule:", "Запланировано:"), "Deadline:", "Срок:")
	}
	if item.Source == "jobs" && detail.Status == "failed" {
		summary = dayText(data.Locale, "Job failed. Provider error details are hidden.", "Задание завершилось ошибкой. Подробности ошибки провайдера скрыты.")
	}
	text += "\n" + security.RedactSensitiveText(summary)
	if detail.Result != "" {
		text += "\n\n" + truncateRunes(security.RedactSensitiveText(detail.Result), 1400)
	}
	buttons := [][]models.InlineKeyboardButton{{{Text: dayText(data.Locale, "Back to list", "Назад к списку"), CallbackData: "dayback:" + card.ID}}}
	// Download is offered on the original card because action binding must remain
	// to that card's immutable Telegram message, not a new unbound detail message.
	if _, deliveryErr := c.bot.SendMessage(ctx, &bot.SendMessageParams{ChatID: card.Chat, MessageThreadID: card.Thread, Text: truncateRunes(text, 1800), ProtectContent: true, ReplyMarkup: models.InlineKeyboardMarkup{InlineKeyboard: buttons}}); deliveryErr != nil {
		c.noteFailure("Telegram SendMessage failed", deliveryErr)
	}
}
func localizedJobStatus(locale, status string) string {
	switch status {
	case "pending":
		return dayText(locale, "Queued", "В очереди")
	case "claimed":
		return dayText(locale, "Assigned", "Назначено")
	case "running":
		return dayText(locale, "Running", "Выполняется")
	case "done":
		return dayText(locale, "Completed", "Завершено")
	case "failed":
		return dayText(locale, "Failed", "Ошибка")
	}
	return dayText(locale, "Status unavailable", "Статус недоступен")
}

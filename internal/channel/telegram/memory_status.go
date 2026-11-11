package telegram

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/i18n"
	"github.com/iulita-ai/iulita/internal/skill"
)

// RegisterStatusCommand reports account memory separately from this conversation.
func (c *Channel) RegisterStatusCommand() {
	c.RegisterCommand("/status", "Show account memory and conversation counts", c.memoryStatus)
}

// handleRegisteredCommand is the real ingress boundary for registered commands.
// The resolved owner comes from registration, never from a command argument or
// Telegram's external numeric ID. Personal replies require the same private route.
func (c *Channel) handleRegisteredCommand(ctx context.Context, m *models.Message, msg channel.IncomingMessage) bool {
	fields := strings.Fields(m.Text)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return false
	}
	name, addressed := c.commandName(fields[0])
	if !addressed {
		return true
	}
	fn, ok := c.commands[name]
	if !ok || fn == nil {
		return false
	}
	owner := msg.ResolvedUserID
	if name == "/status" {
		if owner == "" || !msg.ProvenPrivateRoute || m.From == nil || m.Chat.Type != models.ChatTypePrivate || m.From.ID != m.Chat.ID || msg.UserID != strconv.FormatInt(m.From.ID, 10) {
			return true
		}
		target, available := c.weatherTarget(ctx, msg.ChatID, owner)
		if !available || target.ChatID != m.Chat.ID || target.ThreadID != m.MessageThreadID {
			return true
		}
	}
	commandCtx := i18n.WithLocale(ctx, i18n.ResolveLocale(msg.Locale, msg.LanguageCode))
	commandCtx = skill.WithChatID(commandCtx, msg.ChatID)
	commandCtx = skill.WithUserID(commandCtx, owner)
	if response := fn(commandCtx, msg.ChatID); response != "" {
		if name == "/status" {
			c.sendWeatherText(commandCtx, msg.ChatID, owner, response)
		} else {
			c.sendResponse(m.Chat.ID, response, 0, m.MessageThreadID)
		}
	}
	return true
}

func (c *Channel) memoryStatus(ctx context.Context, conversation string) string {
	owner := skill.UserIDFrom(ctx)
	target, available := c.weatherTarget(ctx, conversation, owner)
	if !available || c.store == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	locale := i18n.LocaleFrom(ctx).String()
	count := func(value int, err error) string {
		if err != nil {
			return dayText(locale, "unavailable", "недоступно")
		}
		return strconv.Itoa(value)
	}
	facts := count(c.store.CountFactsByUser(ctx, owner))
	insights := count(c.store.CountInsightsByUser(ctx, owner))
	messages := count(c.store.CountMessages(ctx, conversation))
	reminders, err := c.store.ListReminders(ctx, conversation)
	pending := count(len(reminders), err)
	local := dayText(locale, "This topic", "Эта тема")
	if target.ThreadID == 0 {
		local = dayText(locale, "This chat", "Этот чат")
	}
	return fmt.Sprintf(dayText(locale,
		"Status\nAccount memory\nFacts: %s\nActive insights: %s\n\n%s\nStored messages: %s\nPending reminders: %s",
		"Статус\nПамять аккаунта\nФакты: %s\nАктивные инсайты: %s\n\n%s\nСохранённые сообщения: %s\nОжидающие напоминания: %s"),
		facts, insights, local, messages, pending)
}

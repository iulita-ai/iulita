package telegram

import (
	"context"
	"strconv"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// handleBusinessManage treats Telegram's management payload as navigation only.
// It never imports peer text, grants policy, runs the assistant or executes an
// account action. Even the generic navigation reply requires a verified owner.
func (c *Channel) handleBusinessManage(ctx context.Context, message *models.Message) bool {
	if message == nil {
		return false
	}
	parts := strings.Fields(message.Text)
	if len(parts) < 2 || strings.SplitN(parts[0], "@", 2)[0] != "/start" || !strings.HasPrefix(parts[1], "bizChat") {
		return false
	}
	if _, validTarget := c.commandName(parts[0]); !validTarget {
		return true
	}
	// Consume malformed/untrusted business management starts rather than passing
	// their payload to ordinary registration, private commands or model context.
	if len(parts) != 2 || message.BusinessConnectionID != "" || message.Chat.Type != models.ChatTypePrivate || message.From == nil || message.From.IsBot || message.From.ID <= 0 || message.From.ID != message.Chat.ID {
		return true
	}
	if len(c.allowedIDs) > 0 {
		if _, allowed := c.allowedIDs[message.From.ID]; !allowed {
			return true
		}
	}
	if c.business == nil || c.store == nil || c.bot == nil || c.botID <= 0 || c.instanceID == "" {
		return true
	}
	value := strings.TrimPrefix(parts[1], "bizChat")
	peerID, err := strconv.ParseInt(value, 10, 64)
	if err != nil || peerID <= 0 || strconv.FormatInt(peerID, 10) != value {
		return true
	}
	resolver, ok := c.store.(businessOwnerResolver)
	if !ok {
		return true
	}
	owner, err := resolver.ResolveTelegramBusinessOwner(ctx, c.instanceID, message.From.ID)
	if err != nil || owner == "" {
		return true
	}
	connections, err := c.business.List(ctx, owner)
	if err != nil {
		return true
	}
	known := false
	for index := range connections {
		state := &connections[index]
		if state.Connection.Key.InstanceID == c.instanceID && state.Connection.Key.BotID == c.botID && state.Connection.OwnerTelegramID == message.From.ID && state.Peers[peerID] != nil {
			known = true
			break
		}
	}
	if !known {
		return true
	}
	text := "Open Iulita → Telegram secretary (/secretary), then select your connected account and this chat. This link only opens management; it does not enable processing or send a reply to the contact."
	if strings.HasPrefix(strings.ToLower(message.From.LanguageCode), "ru") {
		text = "Откройте Iulita → Секретарь Telegram (/secretary), затем выберите подключённый аккаунт и этот чат. Ссылка ведёт только к управлению: она не включает обработку и не отправляет ответ собеседнику."
	}
	// This is an explicit owner command in the bot DM, never an account send.
	// Best effort only: an unavailable owner DM must not fall back to the peer.
	if _, sendErr := c.bot.SendMessage(ctx, &bot.SendMessageParams{ChatID: message.Chat.ID, Text: text, ProtectContent: true}); sendErr != nil && c.logger != nil {
		c.logger.Debug("Telegram business management navigation unavailable")
	}
	return true
}

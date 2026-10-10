package telegram

import (
	"context"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"go.uber.org/zap"
)

// Modern Bot API surfaces (effects, link previews, reactions, deleteMessages,
// checklists) reached through the typed go-telegram/bot methods.

const (
	// effectParty is the stable documented 🎉 message effect ID.
	effectParty = "2947702695341775001"

	// reactionBookmark is the reaction emoji that saves a bot message as a fact.
	reactionBookmark = "🔖"

	// copyTextMaxLen is Telegram's CopyTextButton text limit; a longer payload
	// makes the API reject the whole sendMessage (BUTTON_COPY_TEXT_INVALID).
	copyTextMaxLen = 256
)

// linkPreviewAbove renders link previews above the reply (Bot API 7.0).
func linkPreviewAbove() *models.LinkPreviewOptions {
	return &models.LinkPreviewOptions{ShowAboveText: bot.True()}
}

// --- Keyboard with CopyTextButton (Bot API 7.11) ---

// bookmarkKeyboard builds the 💾 markup plus a 📋 copy button when the full
// response fits Telegram's 256-character copy_text limit; long replies get
// the remember button only.
func bookmarkKeyboard(rememberLabel, copyLabel, callbackData, fullText string) models.InlineKeyboardMarkup {
	row := []models.InlineKeyboardButton{{Text: rememberLabel, CallbackData: callbackData}}
	if utf8.RuneCountInString(fullText) <= copyTextMaxLen {
		row = append(row, models.InlineKeyboardButton{
			Text:     copyLabel,
			CopyText: &models.CopyTextButton{Text: fullText},
		})
	}
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{row}}
}

// --- Recent sent-message tracking (for 🔖 reactions and /clear deletes) ---

type sentMsg struct {
	msgID int
	text  string
	at    time.Time
}

const (
	sentTrackPerChat = 50
	sentTrackTTL     = 48 * time.Hour // deleteMessages rejects older messages
)

type sentTracker struct {
	mu   sync.Mutex
	msgs map[int64][]sentMsg // per Telegram chat, oldest first
}

func newSentTracker() *sentTracker {
	return &sentTracker{msgs: make(map[int64][]sentMsg)}
}

func (t *sentTracker) record(chatID int64, msgID int, text string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	list := t.msgs[chatID]
	list = append(list, sentMsg{msgID: msgID, text: text, at: time.Now()})
	if len(list) > sentTrackPerChat {
		list = list[len(list)-sentTrackPerChat:]
	}
	t.msgs[chatID] = list
}

func (t *sentTracker) text(chatID int64, msgID int) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, m := range t.msgs[chatID] {
		if m.msgID == msgID {
			return m.text, true
		}
	}
	return "", false
}

// deletable returns up to 100 tracked bot message IDs not older than the TTL
// (deleteMessages limit: 100 messages from the last 48 hours).
func (t *sentTracker) deletable(chatID int64) []int {
	cutoff := time.Now().Add(-sentTrackTTL)
	t.mu.Lock()
	defer t.mu.Unlock()
	var ids []int
	var kept []sentMsg
	for _, m := range t.msgs[chatID] {
		if m.at.After(cutoff) && len(ids) < 100 {
			ids = append(ids, m.msgID)
		}
		if m.at.After(cutoff) {
			kept = append(kept, m)
		}
	}
	t.msgs[chatID] = kept
	return ids
}

// --- Send/edit core ---

// sendHTML sends an HTML-formatted message with link previews above the text,
// falling back to plain text when Telegram rejects the HTML. Returns the sent
// message ID.
func (c *Channel) sendHTML(chatID int64, html string, replyTo int, markup models.ReplyMarkup, effectID string) (int, error) {
	p := &bot.SendMessageParams{
		ChatID:             chatID,
		Text:               html,
		ParseMode:          models.ParseModeHTML,
		LinkPreviewOptions: linkPreviewAbove(),
	}
	if replyTo > 0 {
		p.ReplyParameters = &models.ReplyParameters{MessageID: replyTo}
	}
	if markup != nil {
		p.ReplyMarkup = markup
	}
	if effectID != "" {
		p.MessageEffectID = effectID
	}
	m, err := c.bot.SendMessage(context.Background(), p)
	if err != nil {
		c.logger.Debug("html send failed, retrying as plain text", zap.Error(err))
		p.ParseMode = ""
		m, err = c.bot.SendMessage(context.Background(), p)
		if err != nil {
			c.logger.Error("failed to send message", zap.Error(err), zap.Int64("chat_id", chatID))
			return 0, err
		}
	}
	c.sentMsgs.record(chatID, m.ID, html)
	return m.ID, nil
}

// editHTML replaces a message text with HTML content, falling back to plain text.
func (c *Channel) editHTML(chatID int64, msgID int, html string, markup models.ReplyMarkup) {
	p := &bot.EditMessageTextParams{
		ChatID:    chatID,
		MessageID: msgID,
		Text:      html,
		ParseMode: models.ParseModeHTML,
	}
	if markup != nil {
		p.ReplyMarkup = markup
	}
	if _, err := c.bot.EditMessageText(context.Background(), p); err != nil {
		c.logger.Debug("html edit failed, retrying as plain text", zap.Error(err))
		p.ParseMode = ""
		if _, err := c.bot.EditMessageText(context.Background(), p); err != nil {
			c.logger.Debug("plain edit failed", zap.Error(err), zap.Int64("chat_id", chatID), zap.Int("message_id", msgID))
		}
	}
}

// --- Reactions (Bot API 7.0) ---

// setReaction sets (or, for an empty emoji, removes) the bot's reaction on a
// message. Failures are logged at debug level: reactions are a soft UX layer.
func (c *Channel) setReaction(chatID int64, msgID int, emoji string) {
	p := &bot.SetMessageReactionParams{ChatID: chatID, MessageID: msgID}
	if emoji == "" {
		p.Reaction = []models.ReactionType{}
	} else {
		p.Reaction = []models.ReactionType{{
			Type:              models.ReactionTypeTypeEmoji,
			ReactionTypeEmoji: &models.ReactionTypeEmoji{Type: models.ReactionTypeTypeEmoji, Emoji: emoji},
		}}
	}
	if _, err := c.bot.SetMessageReaction(context.Background(), p); err != nil {
		c.logger.Debug("set reaction failed", zap.Error(err), zap.Int64("chat_id", chatID), zap.Int("message_id", msgID))
	}
}

// deleteBotMessages removes tracked bot messages from the last 48 hours
// (deleteMessages, Bot API 7.0). Used by /clear.
func (c *Channel) deleteBotMessages(chatID int64) {
	ids := c.sentMsgs.deletable(chatID)
	if len(ids) == 0 {
		return
	}
	if _, err := c.bot.DeleteMessages(context.Background(), &bot.DeleteMessagesParams{
		ChatID:     chatID,
		MessageIDs: ids,
	}); err != nil {
		c.logger.Debug("delete messages failed", zap.Error(err), zap.Int64("chat_id", chatID), zap.Int("count", len(ids)))
	}
}

// handleReactionUpdate saves a bot message as a fact when the user reacts
// with 🔖, and confirms with a ✅ reaction.
func (c *Channel) handleReactionUpdate(ctx context.Context, r *models.MessageReactionUpdated) {
	if c.rememberSvc == nil || r.User == nil || !c.isAllowed(r.User.ID) {
		return
	}
	bookmarked := false
	for _, rt := range r.NewReaction {
		if rt.Type == models.ReactionTypeTypeEmoji && rt.ReactionTypeEmoji != nil && rt.ReactionTypeEmoji.Emoji == reactionBookmark {
			bookmarked = true
			break
		}
	}
	if !bookmarked {
		return
	}
	text, ok := c.sentMsgs.text(r.Chat.ID, r.MessageID)
	if !ok {
		return
	}
	chatIDStr := fmt.Sprintf("%d", r.Chat.ID)
	userID := chatIDStr
	if c.userResolver != nil {
		resolvedID, err := c.userResolver.ResolveUser(ctx, "telegram", fmt.Sprintf("%d", r.User.ID), r.User.Username, chatIDStr)
		if err != nil {
			c.logger.Warn("reaction user resolution failed", zap.Error(err), zap.Int64("user_id", r.User.ID))
			return
		}
		userID = resolvedID
	}
	if _, err := c.rememberSvc.Save(ctx, chatIDStr, userID, text); err != nil {
		c.logger.Error("reaction bookmark save failed", zap.Error(err), zap.Int64("chat_id", r.Chat.ID))
		return
	}
	c.setReaction(r.Chat.ID, r.MessageID, "✅")
}

// --- Checklists (Bot API 9.1) ---

// SendChecklist sends a native Telegram checklist to a chat. Task completion
// is tracked client-side by Telegram; this is a one-way mirror surface.
// Returns the sent message ID.
func (c *Channel) SendChecklist(_ context.Context, chatID int64, title string, tasks []string) (int, error) {
	if len(tasks) == 0 {
		return 0, fmt.Errorf("checklist needs at least one task")
	}
	input := make([]models.InputChecklistTask, 0, len(tasks))
	for i, t := range tasks {
		input = append(input, models.InputChecklistTask{ID: i + 1, Text: t})
	}
	m, err := c.bot.SendChecklist(context.Background(), &bot.SendChecklistParams{
		ChatID: chatID,
		Checklist: models.InputChecklist{
			Title: title,
			Tasks: input,
		},
	})
	if err != nil {
		return 0, fmt.Errorf("sending checklist: %w", sanitizeTGError(err))
	}
	return m.ID, nil
}

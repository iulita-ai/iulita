package telegram

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"go.uber.org/zap"
)

// Bot API features newer than the v5 library are reached through raw
// MakeRequest calls (Params is map[string]string, JSON payloads are passed
// as strings). Effect IDs are the stable documented ones.

const (
	effectParty = "2947702695341775001" // 🎉

	// linkPreviewAbove renders link previews above the reply (Bot API 7.0).
	linkPreviewAbove = `{"show_above_text":true}`

	// reactionBookmark is the reaction emoji that saves a bot message as a fact.
	reactionBookmark = "🔖"
)

// --- Keyboard with CopyTextButton (Bot API 7.11) ---

type copyText struct {
	Text string `json:"text"`
}

type kbButton struct {
	Text         string    `json:"text"`
	CopyText     *copyText `json:"copy_text,omitempty"`
	CallbackData *string   `json:"callback_data,omitempty"`
}

type inlineKeyboard struct {
	InlineKeyboard [][]kbButton `json:"inline_keyboard"`
}

// bookmarkKeyboard builds the 💾 + 📋 markup: the remember callback button and
// a client-side copy button carrying the full response text.
func bookmarkKeyboard(rememberLabel, copyLabel, callbackData, fullText string) string {
	data := callbackData
	kb := inlineKeyboard{InlineKeyboard: [][]kbButton{{
		{Text: rememberLabel, CallbackData: &data},
		{Text: copyLabel, CopyText: &copyText{Text: fullText}},
	}}}
	b, err := json.Marshal(kb)
	if err != nil {
		return ""
	}
	return string(b)
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

// --- Raw send/edit core ---

// sendHTML sends an HTML-formatted message via the raw API so that
// link_preview_options, message effects and CopyTextButton markups are
// available. Falls back to plain text when Telegram rejects the HTML.
// Returns the sent message ID.
func (c *Channel) sendHTML(chatID int64, html string, replyTo int, markup, effectID string) (int, error) {
	params := tgbotapi.Params{
		"chat_id":              strconv.FormatInt(chatID, 10),
		"text":                 html,
		"parse_mode":           tgbotapi.ModeHTML,
		"link_preview_options": linkPreviewAbove,
	}
	if replyTo > 0 {
		params["reply_to_message_id"] = strconv.Itoa(replyTo)
	}
	if markup != "" {
		params["reply_markup"] = markup
	}
	if effectID != "" {
		params["message_effect_id"] = effectID
	}
	resp, err := c.bot.MakeRequest("sendMessage", params)
	if err != nil {
		c.logger.Debug("html send failed, retrying as plain text", zap.Error(err))
		delete(params, "parse_mode")
		resp, err = c.bot.MakeRequest("sendMessage", params)
		if err != nil {
			c.logger.Error("failed to send message", zap.Error(err), zap.Int64("chat_id", chatID))
			return 0, err
		}
	}
	var m struct {
		MessageID int `json:"message_id"`
	}
	if jsonErr := json.Unmarshal(resp.Result, &m); jsonErr != nil {
		c.logger.Debug("cannot decode sent message", zap.Error(jsonErr))
		return m.MessageID, nil
	}
	c.sentMsgs.record(chatID, m.MessageID, html)
	return m.MessageID, nil
}

// editHTML replaces a message text with HTML content (raw API for markup
// support), falling back to plain text.
func (c *Channel) editHTML(chatID int64, msgID int, html, markup string) {
	params := tgbotapi.Params{
		"chat_id":    strconv.FormatInt(chatID, 10),
		"message_id": strconv.Itoa(msgID),
		"text":       html,
		"parse_mode": tgbotapi.ModeHTML,
	}
	if markup != "" {
		params["reply_markup"] = markup
	}
	if _, err := c.bot.MakeRequest("editMessageText", params); err != nil {
		c.logger.Debug("html edit failed, retrying as plain text", zap.Error(err))
		delete(params, "parse_mode")
		if _, err := c.bot.MakeRequest("editMessageText", params); err != nil {
			c.logger.Debug("plain edit failed", zap.Error(err), zap.Int64("chat_id", chatID), zap.Int("message_id", msgID))
		}
	}
}

// --- Reactions (Bot API 7.0) ---

// setReaction sets (or, for an empty emoji, removes) the bot's reaction on a
// message. Failures are logged at debug level: reactions are a soft UX layer.
func (c *Channel) setReaction(chatID int64, msgID int, emoji string) {
	params := tgbotapi.Params{
		"chat_id":    strconv.FormatInt(chatID, 10),
		"message_id": strconv.Itoa(msgID),
	}
	if emoji == "" {
		params["reaction"] = "[]"
	} else {
		params["reaction"] = `[{"type":"emoji","emoji":` + strconv.Quote(emoji) + `}]`
	}
	if _, err := c.bot.MakeRequest("setMessageReaction", params); err != nil {
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
	raw, err := json.Marshal(ids)
	if err != nil {
		return
	}
	params := tgbotapi.Params{
		"chat_id":     strconv.FormatInt(chatID, 10),
		"message_ids": string(raw),
	}
	if _, err := c.bot.MakeRequest("deleteMessages", params); err != nil {
		c.logger.Debug("delete messages failed", zap.Error(err), zap.Int64("chat_id", chatID), zap.Int("count", len(ids)))
	}
}

// --- Reaction updates (raw getUpdates decode) ---

type reactionType struct {
	Type  string `json:"type"`
	Emoji string `json:"emoji,omitempty"`
}

type messageReactionUpdate struct {
	Chat        tgbotapi.Chat  `json:"chat"`
	From        *tgbotapi.User `json:"from"`
	MessageID   int            `json:"message_id"`
	NewReaction []reactionType `json:"new_reaction"`
}

// rawUpdate extends the library Update with newer update types the library
// drops during decoding.
type rawUpdate struct {
	tgbotapi.Update
	MessageReaction *messageReactionUpdate `json:"message_reaction,omitempty"`
}

// allowedUpdates pins the update types we consume; message_reaction is NOT
// in the server's default set.
const allowedUpdates = `["message","edited_message","callback_query","message_reaction"]`

// pollUpdates replaces the library update channel with raw getUpdates so
// message_reaction updates survive decoding. Reactions are handled inline;
// everything else is forwarded as library Updates.
func (c *Channel) pollUpdates(ctx context.Context) <-chan tgbotapi.Update {
	out := make(chan tgbotapi.Update, 64)
	go func() {
		defer close(out)
		offset := 0
		for {
			if ctx.Err() != nil {
				return
			}
			params := tgbotapi.Params{
				"offset":          strconv.Itoa(offset),
				"timeout":         "30",
				"allowed_updates": allowedUpdates,
			}
			resp, err := c.bot.MakeRequest("getUpdates", params)
			if err != nil {
				c.logger.Warn("getUpdates failed", zap.Error(err))
				select {
				case <-ctx.Done():
					return
				case <-time.After(3 * time.Second):
				}
				continue
			}
			var updates []rawUpdate
			if err := json.Unmarshal(resp.Result, &updates); err != nil {
				c.logger.Warn("decode updates failed", zap.Error(err))
				continue
			}
			for i := range updates {
				offset = updates[i].UpdateID + 1
				if r := updates[i].MessageReaction; r != nil {
					c.handleReactionUpdate(ctx, r)
					continue
				}
				select {
				case out <- updates[i].Update:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out
}

// handleReactionUpdate saves a bot message as a fact when the user reacts
// with 🔖, and confirms with a ✅ reaction.
func (c *Channel) handleReactionUpdate(ctx context.Context, r *messageReactionUpdate) {
	if c.rememberSvc == nil || r.From == nil || !c.isAllowed(r.From.ID) {
		return
	}
	bookmarked := false
	for _, rt := range r.NewReaction {
		if rt.Type == "emoji" && rt.Emoji == reactionBookmark {
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
	chatIDStr := strconv.FormatInt(r.Chat.ID, 10)
	userID := chatIDStr
	if c.userResolver != nil {
		resolvedID, err := c.userResolver.ResolveUser(ctx, "telegram", strconv.FormatInt(r.From.ID, 10), r.From.UserName, chatIDStr)
		if err != nil {
			c.logger.Warn("reaction user resolution failed", zap.Error(err), zap.Int64("user_id", r.From.ID))
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

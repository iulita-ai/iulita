package telegram

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/bookmark"
	"github.com/iulita-ai/iulita/internal/i18n"
)

// rememberEntry stores metadata for a pending bookmark button.
type rememberEntry struct {
	tgChatID  int64
	tgUserID  int64 // Telegram user ID who owns this bookmark (for security validation)
	msgID     int
	content   string // full response text (all chunks combined)
	chatID    string // iulita chat ID (e.g. "telegram:12345")
	userID    string // iulita user UUID
	locale    string // for i18n of callback answer
	createdAt time.Time
}

// rememberState tracks pending bookmark buttons.
type rememberState struct {
	mu      sync.Mutex
	entries map[string]*rememberEntry // callbackData → entry
	blocked map[string]bool
}

func newRememberState() *rememberState {
	return &rememberState{
		entries: make(map[string]*rememberEntry),
	}
}

func (rs *rememberState) store(key string, entry *rememberEntry) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.blocked[entry.chatID] {
		return
	}
	rs.entries[key] = entry
}

// takeBound validates the actor and original message before consuming a button.
func (rs *rememberState) takeBound(key string, cq *models.CallbackQuery) (*rememberEntry, bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	e, ok := rs.entries[key]
	if !ok || cq.Message.Message == nil {
		return nil, false
	}
	m := cq.Message.Message
	if e.tgUserID == 0 || e.tgUserID != cq.From.ID || e.tgChatID != m.Chat.ID || e.msgID != m.ID || time.Since(e.createdAt) > 30*time.Minute {
		return nil, false
	}
	delete(rs.entries, key)
	return e, true
}

// startCleanup removes stale entries every 5 minutes.
func (rs *rememberState) startCleanup(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rs.mu.Lock()
			for k, e := range rs.entries {
				if time.Since(e.createdAt) > 30*time.Minute {
					delete(rs.entries, k)
				}
			}
			rs.mu.Unlock()
		}
	}
}

// SetBookmarkService attaches a bookmark service for the "remember" button feature.
func (c *Channel) SetBookmarkService(svc bookmark.Service) {
	c.rememberSvc = svc
}

// HandleRememberCallback processes a "remember:..." callback query.
// Returns true if the callback was handled.
func (c *Channel) HandleRememberCallback(cq *models.CallbackQuery) bool {
	// Acknowledge "noop" callbacks (from already-saved ✅ button).
	if cq.Data == "noop" {
		_, _ = c.bot.AnswerCallbackQuery(context.Background(), &bot.AnswerCallbackQueryParams{ //nolint:errcheck // ack only
			CallbackQueryID: cq.ID,
		})
		return true
	}

	if !strings.HasPrefix(cq.Data, "remember:") {
		return false
	}

	entry, ok := c.remembers.takeBound(cq.Data, cq)
	if !ok {
		// Already handled or expired.
		tag := i18n.ResolveLocale("", "en")
		_, _ = c.bot.AnswerCallbackQuery(context.Background(), &bot.AnswerCallbackQueryParams{ //nolint:errcheck // ack only
			CallbackQueryID: cq.ID,
			Text:            i18n.Tl(tag, "TelegramBookmarkUnavailable"),
		})
		return true
	}

	// Verify the callback sender matches the message recipient (security: prevent cross-user bookmarks).
	if entry.tgUserID != 0 && cq.From.ID != entry.tgUserID {
		c.logger.Warn("bookmark ownership mismatch",
			zap.Int64("requested_by", cq.From.ID),
			zap.Int64("owned_by", entry.tgUserID))
		_, _ = c.bot.AnswerCallbackQuery(context.Background(), &bot.AnswerCallbackQueryParams{ //nolint:errcheck // ack only
			CallbackQueryID: cq.ID,
		})
		return true
	}

	tag := i18n.ResolveLocale(entry.locale, "en")

	ctx := context.Background()
	_, err := c.rememberSvc.Save(ctx, entry.chatID, entry.userID, entry.content)
	if err != nil {
		c.logger.Error("bookmark save failed",
			zap.Error(err),
			zap.String("chat_id", entry.chatID))
		_, _ = c.bot.AnswerCallbackQuery(context.Background(), &bot.AnswerCallbackQueryParams{ //nolint:errcheck // error toast
			CallbackQueryID: cq.ID,
			Text:            i18n.Tl(tag, "BookmarkError"),
		})
		return true
	}

	// Acknowledge the callback with a toast.
	_, _ = c.bot.AnswerCallbackQuery(context.Background(), &bot.AnswerCallbackQueryParams{ //nolint:errcheck // ack only
		CallbackQueryID: cq.ID,
		Text:            i18n.Tl(tag, "BookmarkSaved"),
	})

	// Update button to show ✅.
	savedLabel := i18n.Tl(tag, "BookmarkSaved")
	noopData := "noop"
	savedKB := models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{{
			{Text: savedLabel, CallbackData: noopData},
		}},
	}
	_, _ = c.bot.EditMessageReplyMarkup(ctx, &bot.EditMessageReplyMarkupParams{ //nolint:errcheck // best-effort UI update
		ChatID:      entry.tgChatID,
		MessageID:   entry.msgID,
		ReplyMarkup: savedKB,
	})

	// Remove the keyboard after a short delay.
	go func() {
		time.Sleep(3 * time.Second)
		_, _ = c.bot.EditMessageReplyMarkup(context.Background(), &bot.EditMessageReplyMarkupParams{ //nolint:errcheck // best-effort UI cleanup
			ChatID:      entry.tgChatID,
			MessageID:   entry.msgID,
			ReplyMarkup: models.InlineKeyboardMarkup{},
		})
	}()

	return true
}

// sendResponseWithBookmark sends a response with a 💾 bookmark button on the last chunk.
// fullText is the complete response (all chunks) saved as the bookmark content.
func (c *Channel) sendResponseWithBookmark(chatID int64, text string, replyTo int, chatIDStr, userID, locale string) {
	target, err := c.resolveTarget(context.Background(), chatIDStr)
	if err != nil {
		return
	}
	// fullContent keeps the original Markdown (bookmark saves raw assistant output);
	// only the displayed chunks are converted to Telegram's HTML dialect.
	chunks := telegramHTMLChunks(text)
	for i, chunk := range chunks {
		rt := 0
		if i == 0 {
			rt = replyTo
		}
		if i == len(chunks)-1 {
			// Last chunk: attach the bookmark + copy buttons.
			c.sendSingleMessageWithBookmark(chatID, chunk, rt, text, chatIDStr, userID, locale)
		} else {
			_, _ = c.sendHTML(chatID, chunk, rt, nil, "", target.ThreadID) //nolint:errcheck // sendHTML logs failures
		}
	}
}

// sendSingleMessageWithBookmark sends a message with inline 💾 remember and
// 📋 copy buttons (the copy button carries the full response, Bot API 7.11).
func (c *Channel) sendSingleMessageWithBookmark(chatID int64, text string, replyTo int, fullContent, chatIDStr, userID, locale string) {
	target, err := c.resolveTarget(context.Background(), chatIDStr)
	if err != nil {
		return
	}
	nonce := generateNonce()
	cbData := "remember:" + nonce

	tag := i18n.ResolveLocale(locale, "en")
	markup := bookmarkKeyboard(
		i18n.Tl(tag, "BookmarkButton"),
		i18n.Tl(tag, "CopyButton"),
		cbData,
		fullContent,
	)

	msgID, err := c.sendHTML(chatID, text, replyTo, markup, "", target.ThreadID)
	if err != nil {
		return
	}

	c.remembers.store(cbData, &rememberEntry{
		tgChatID:  chatID,
		tgUserID:  c.bookmarkActor(chatIDStr, chatID),
		msgID:     msgID,
		content:   fullContent,
		chatID:    chatIDStr,
		userID:    userID,
		locale:    locale,
		createdAt: time.Now(),
	})
}

// StartStreamWithBookmark implements channel.BookmarkStreamingSender.
func (c *Channel) StartStreamWithBookmark(ctx context.Context, chatID string, replyTo int, userID string) (editFn, doneFn func(string), err error) {
	return c.bufferedStream(ctx, chatID, replyTo, userID)
}

// generateNonce creates a short random hex string for callback data.
func generateNonce() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// bookmarkActor never substitutes a group ID for the initiating user.
func (c *Channel) bookmarkActor(conversationID string, chatID int64) int64 {
	if actor := c.turnSender(conversationID); actor != 0 {
		return actor
	}
	if chatID > 0 {
		return chatID
	}
	return 0
}

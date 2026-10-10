package telegram

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
}

func newRememberState() *rememberState {
	return &rememberState{
		entries: make(map[string]*rememberEntry),
	}
}

func (rs *rememberState) store(key string, entry *rememberEntry) {
	rs.mu.Lock()
	rs.entries[key] = entry
	rs.mu.Unlock()
}

func (rs *rememberState) take(key string) (*rememberEntry, bool) {
	rs.mu.Lock()
	e, ok := rs.entries[key]
	if ok {
		delete(rs.entries, key)
	}
	rs.mu.Unlock()
	return e, ok
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

	entry, ok := c.remembers.take(cq.Data)
	if !ok {
		// Already handled or expired.
		tag := i18n.ResolveLocale("", "en")
		_, _ = c.bot.AnswerCallbackQuery(context.Background(), &bot.AnswerCallbackQueryParams{ //nolint:errcheck // ack only
			CallbackQueryID: cq.ID,
			Text:            i18n.Tl(tag, "BookmarkAlreadySaved"),
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
			_, _ = c.sendHTML(chatID, chunk, rt, nil, "") //nolint:errcheck // sendHTML logs failures
		}
	}
}

// sendSingleMessageWithBookmark sends a message with inline 💾 remember and
// 📋 copy buttons (the copy button carries the full response, Bot API 7.11).
func (c *Channel) sendSingleMessageWithBookmark(chatID int64, text string, replyTo int, fullContent, chatIDStr, userID, locale string) {
	nonce := generateNonce()
	cbData := "remember:" + nonce

	tag := i18n.ResolveLocale(locale, "en")
	markup := bookmarkKeyboard(
		i18n.Tl(tag, "BookmarkButton"),
		i18n.Tl(tag, "CopyButton"),
		cbData,
		fullContent,
	)

	msgID, err := c.sendHTML(chatID, text, replyTo, markup, "")
	if err != nil {
		return
	}

	c.remembers.store(cbData, &rememberEntry{
		tgChatID:  chatID,
		tgUserID:  chatID, // in DM, chatID == userID; in groups, still the recipient
		msgID:     msgID,
		content:   fullContent,
		chatID:    chatIDStr,
		userID:    userID,
		locale:    locale,
		createdAt: time.Now(),
	})
}

// StartStreamWithBookmark implements channel.BookmarkStreamingSender.
func (c *Channel) StartStreamWithBookmark(ctx context.Context, chatID string, replyTo int, userID string) (func(string), func(string), error) { //nolint:gocritic // naming return values reduces readability here
	tgChatID, err := strconv.ParseInt(chatID, 10, 64)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid chat ID: %w", err)
	}

	// Reuse the status message if it was consumed by stream_start AND the task was quick.
	var msgID int
	if entry, ok := c.statusMsgs.get(chatID); ok && entry.isConsumed() && !entry.isLongTask() {
		msgID = entry.getMsgID()
		c.statusMsgs.remove(chatID)
		_, _ = c.bot.EditMessageText(context.Background(), &bot.EditMessageTextParams{ //nolint:errcheck // best-effort placeholder
			ChatID:    tgChatID,
			MessageID: msgID,
			Text:      "...",
		})
	} else {
		if entry, ok := c.statusMsgs.get(chatID); ok && entry.isConsumed() {
			c.finalizeStatusMessage(chatID, entry)
		}
		p := &bot.SendMessageParams{ChatID: tgChatID, Text: "..."}
		if replyTo > 0 {
			p.ReplyParameters = &models.ReplyParameters{MessageID: replyTo}
		}
		sent, sendErr := c.bot.SendMessage(context.Background(), p)
		if sendErr != nil {
			return nil, nil, fmt.Errorf("sending initial stream message: %w", sendErr)
		}
		msgID = sent.ID
	}

	var lastEditNs atomic.Int64
	var done atomic.Bool

	// Resolve locale from context for button label.
	locale := ""
	if tag := i18n.LocaleFrom(ctx); tag.String() != "und" {
		locale = tag.String()
	}

	editFn := func(text string) {
		if done.Load() {
			return
		}
		now := time.Now().UnixNano()
		if time.Duration(now-lastEditNs.Load()) < 1500*time.Millisecond {
			return // coalesce edits
		}
		_, err := c.bot.EditMessageText(context.Background(), &bot.EditMessageTextParams{
			ChatID:    tgChatID,
			MessageID: msgID,
			Text:      text,
		})
		if err != nil {
			c.logger.Debug("stream edit failed", zap.Error(err))
		}
		lastEditNs.Store(time.Now().UnixNano())
	}

	doneFn := func(text string) {
		done.Store(true)

		// Display text is converted to Telegram's HTML dialect;
		// the stored bookmark content keeps the original Markdown.
		var markup models.ReplyMarkup
		if c.rememberSvc != nil {
			nonce := generateNonce()
			cbData := "remember:" + nonce

			tag := i18n.ResolveLocale(locale, "en")
			markup = bookmarkKeyboard(
				i18n.Tl(tag, "BookmarkButton"),
				i18n.Tl(tag, "CopyButton"),
				cbData,
				text,
			)

			c.remembers.store(cbData, &rememberEntry{
				tgChatID:  tgChatID,
				tgUserID:  tgChatID, // in DM, chatID == userID
				msgID:     msgID,
				content:   text,
				chatID:    chatID,
				userID:    userID,
				locale:    locale,
				createdAt: time.Now(),
			})
		}

		c.editHTML(tgChatID, msgID, toTelegramHTML(text), markup)
		c.sentMsgs.record(tgChatID, msgID, text)
	}

	return editFn, doneFn, nil
}

// generateNonce creates a short random hex string for callback data.
func generateNonce() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

package telegram

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/bookmark"
	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/i18n"
	"github.com/iulita-ai/iulita/internal/ratelimit"
	"github.com/iulita-ai/iulita/internal/storage"
)

// ClearFunc is called to clear conversation history for a chat.
type ClearFunc func(ctx context.Context, chatID string) error

// CommandFunc handles a chat command, returning the response text.
type CommandFunc func(ctx context.Context, chatID string) string

// TranscriptionProvider transcribes audio data to text.
type TranscriptionProvider interface {
	Transcribe(ctx context.Context, audio []byte, format string) (string, error)
}

// Channel implements channel.InputChannel for Telegram.
type Channel struct {
	bot            *bot.Bot
	http           *http.Client // shared client for file downloads
	instanceID     string       // channel instance slug (e.g., "tg-config")
	allowedIDs     map[int64]struct{}
	clearFn        ClearFunc
	commands       map[string]CommandFunc
	commandOrder   []models.BotCommand
	debounceWindow time.Duration
	rateLimiter    *ratelimit.Limiter    // nil = no rate limiting
	userResolver   channel.UserResolver  // nil = no user resolution (backward compat)
	store          storage.Repository    // nil = no locale lookup
	transcriber    TranscriptionProvider // nil = voice messages ignored
	prompts        *promptState          // interactive skill prompts
	rememberSvc    bookmark.Service      // nil = no bookmark feature
	remembers      *rememberState        // pending bookmark buttons
	statusMsgs     *statusState          // live status messages per chat
	sentMsgs       *sentTracker          // recent sent texts (🔖 reactions, /clear deletes)
	locLimiter     *ratelimit.Limiter    // outbound location sends: 10/hour/chat (set in New)
	logger         *zap.Logger
	wg             sync.WaitGroup // tracks in-flight message processing
}

// SetRateLimiter attaches a per-chat rate limiter. Nil disables limiting.
func (c *Channel) SetRateLimiter(rl *ratelimit.Limiter) {
	c.rateLimiter = rl
}

// SetUserResolver attaches a user resolver for mapping Telegram users to iulita users.
func (c *Channel) SetUserResolver(ur channel.UserResolver) {
	c.userResolver = ur
}

// SetStore attaches a storage repository for locale lookups.
func (c *Channel) SetStore(s storage.Repository) {
	c.store = s
}

// SetTranscriber attaches a voice message transcription provider.
func (c *Channel) SetTranscriber(t TranscriptionProvider) {
	c.transcriber = t
}

// SetInstanceID sets the channel instance ID for this Telegram bot.
func (c *Channel) SetInstanceID(id string) {
	c.instanceID = id
}

// RegisterCommand adds a slash command handler with a description for the Telegram menu.
func (c *Channel) RegisterCommand(name, description string, fn CommandFunc) {
	c.commands[name] = fn
	c.commandOrder = append(c.commandOrder, models.BotCommand{
		Command:     strings.TrimPrefix(name, "/"),
		Description: description,
	})
}

// New creates a new Telegram channel.
func New(token string, allowedIDs []int64, clearFn ClearFunc, debounceWindow time.Duration, httpClient *http.Client, logger *zap.Logger) (*Channel, error) {
	client := httpClient
	if client == nil {
		client = &http.Client{Timeout: 100 * time.Second}
	}
	opts := []bot.Option{
		// Long-poll window for getUpdates; the client must outlive it.
		bot.WithHTTPClient(60*time.Second, client),
		// Sequential dispatch: preserves the per-update ordering semantics
		// the debounce/prompt state machines rely on.
		bot.WithNotAsyncHandlers(),
		bot.WithWorkers(1),
	}
	b, err := bot.New(token, opts...)
	if err != nil {
		return nil, fmt.Errorf("creating telegram bot: %w", err)
	}
	me, err := b.GetMe(context.Background())
	if err != nil {
		return nil, fmt.Errorf("telegram getMe: %w", err)
	}

	allowed := make(map[int64]struct{}, len(allowedIDs))
	for _, id := range allowedIDs {
		allowed[id] = struct{}{}
	}

	logger.Info("telegram bot authorized", zap.String("username", me.Username))

	return &Channel{
		bot:            b,
		http:           client,
		allowedIDs:     allowed,
		clearFn:        clearFn,
		commands:       make(map[string]CommandFunc),
		debounceWindow: debounceWindow,
		prompts:        newPromptState(),
		remembers:      newRememberState(),
		statusMsgs:     newStatusState(),
		sentMsgs:       newSentTracker(),
		locLimiter:     ratelimit.New(MaxLocationSendsPerHour, time.Hour),
		logger:         logger,
	}, nil
}

func (c *Channel) Start(ctx context.Context, handler channel.MessageHandler) error {
	// Register commands in Telegram's menu (the "/" button).
	if len(c.commandOrder) > 0 {
		allCmds := append([]models.BotCommand{
			{Command: "clear", Description: i18n.Tl(i18n.ResolveLocale("", "en"), "TelegramClearCommand")},
		}, c.commandOrder...)
		if _, err := c.bot.SetMyCommands(ctx, &bot.SetMyCommandsParams{Commands: allCmds}); err != nil {
			c.logger.Error("failed to set bot commands menu", zap.Error(err))
		} else {
			c.logger.Info("registered bot commands in Telegram menu", zap.Int("count", len(allCmds)))
		}
	}

	// processMsg handles a (possibly debounced/merged) message.
	processMsg := func(msg channel.IncomingMessage) {
		tgChatID, _ := strconv.ParseInt(msg.ChatID, 10, 64)

		// Pre-register replyTo for status message threading.
		c.statusMsgs.setReplyTo(msg.ChatID, msg.MessageID)

		// 👀 ack: the user sees processing started; cleared once the reply lands.
		c.setReaction(tgChatID, msg.MessageID, "👀")

		handlerCtx := context.WithoutCancel(ctx)
		typingCtx, stopTyping := context.WithCancel(handlerCtx)
		go c.keepTyping(typingCtx, tgChatID)

		c.wg.Add(1)
		response, err := handler(handlerCtx, msg)
		stopTyping()
		c.wg.Done()
		c.setReaction(tgChatID, msg.MessageID, "")

		// Check if remember skill was used (skip bookmark button if so).
		skipBookmark := false
		if entry, ok := c.statusMsgs.get(msg.ChatID); ok {
			entry.mu.Lock()
			skipBookmark = entry.skipBookmark
			entry.mu.Unlock()
		}

		c.deleteStatusMessage(msg.ChatID, tgChatID)
		if err != nil {
			c.logger.Error("handler error", zap.Error(err), zap.String("chat_id", msg.ChatID))
			localeCtx := i18n.WithLocale(handlerCtx, i18n.ResolveLocale(msg.Locale, msg.LanguageCode))
			response = i18n.T(localeCtx, "TelegramErrorResponse")
		}

		if c.rememberSvc != nil && response != "" && !skipBookmark && err == nil {
			c.sendResponseWithBookmark(tgChatID, response, msg.MessageID, msg.ChatID, msg.ResolvedUserID, msg.Locale)
		} else {
			c.sendResponse(tgChatID, response, msg.MessageID)
		}
	}

	debounce := newDebouncer(c.debounceWindow, processMsg)
	if c.debounceWindow > 0 {
		c.logger.Info("message debouncing enabled", zap.Duration("window", c.debounceWindow))
	}

	// Health monitor: periodic bot.GetMe() check.
	go c.healthMonitor(ctx)

	// Cleanup stale remember entries.
	go c.remembers.startCleanup(ctx)

	// dispatch processes one update. Reaction updates are handled inline; the
	// library's long-poll loop owns fetching and offset management.
	dispatch := func(update models.Update) {
		// Handle inline keyboard callback queries (bookmark + interactive prompts).
		if update.CallbackQuery != nil {
			if c.isAllowed(update.CallbackQuery.From.ID) {
				// Bookmark button takes priority.
				if c.rememberSvc != nil && c.HandleRememberCallback(update.CallbackQuery) {
					return
				}
				if c.prompts.HandleCallback(c.bot, update.CallbackQuery) {
					return
				}
			}
			return
		}

		// 🔖 reaction on a bot message saves it as a fact.
		if update.MessageReaction != nil {
			c.handleReactionUpdate(ctx, update.MessageReaction)
			return
		}

		if update.Message == nil {
			// Live-location position updates arrive as edited_message and are
			// deliberately ignored in v1 (initial pin only). One Debug line keeps
			// "ignored by design" distinguishable from "dropped by bug".
			if update.EditedMessage != nil && update.EditedMessage.Location != nil {
				c.logger.Debug("live location update ignored (v1: initial pin only)",
					zap.String("chat_id", strconv.FormatInt(update.EditedMessage.Chat.ID, 10)),
					zap.Int("message_id", update.EditedMessage.ID))
			}
			return
		}

		hasText := update.Message.Text != ""
		hasPhoto := len(update.Message.Photo) > 0
		hasDocument := update.Message.Document != nil
		hasVoice := update.Message.Voice != nil
		hasAudio := update.Message.Audio != nil
		hasLocation := update.Message.Location != nil || update.Message.Venue != nil
		if !hasUpdateContent(update.Message) {
			return
		}

		userID := update.Message.From.ID
		if !c.isAllowed(userID) {
			c.logger.Warn("unauthorized user", zap.Int64("user_id", userID))
			return
		}

		chatID := strconv.FormatInt(update.Message.Chat.ID, 10)

		// Handle /clear command.
		if update.Message.Text == "/clear" {
			c.handleClear(ctx, update.Message.Chat.ID, chatID)
			return
		}

		// Handle registered commands.
		if strings.HasPrefix(update.Message.Text, "/") {
			cmd := strings.Fields(update.Message.Text)[0]
			if fn, ok := c.commands[cmd]; ok {
				resp := fn(ctx, chatID)
				if resp != "" {
					c.sendResponse(update.Message.Chat.ID, resp, 0)
				}
				return
			}
		}

		// Check if this text should be routed to a pending interactive prompt.
		if hasText && c.prompts.HandleText(update.Message.Chat.ID, update.Message.Text) {
			return
		}

		// Extract text: Text for text messages, Caption for photo/document messages.
		text := update.Message.Text
		if text == "" && (hasPhoto || hasDocument) {
			text = update.Message.Caption
		}

		msg := channel.IncomingMessage{
			ChatID:            chatID,
			UserID:            strconv.FormatInt(userID, 10),
			ChannelInstanceID: c.instanceID,
			UserName:          update.Message.From.Username,
			Text:              text,
			LanguageCode:      update.Message.From.LanguageCode,
			MessageID:         update.Message.ID,
			Caps:              channel.CapStreaming | channel.CapMarkdown | channel.CapTyping | channel.CapButtons | channel.CapLocations,
		}

		// Resolve iulita user from channel binding.
		if c.userResolver != nil {
			resolvedID, err := c.userResolver.ResolveUser(ctx, "telegram", msg.UserID, msg.UserName, chatID)
			if err != nil {
				c.logger.Warn("user resolution failed", zap.Error(err), zap.String("user_id", msg.UserID))
				localeCtx := i18n.WithLocale(ctx, i18n.ResolveLocale("", msg.LanguageCode))
				c.sendSingleMessage(update.Message.Chat.ID, i18n.T(localeCtx, "TelegramRegistrationNotAllowed"), 0)
				return
			}
			msg.ResolvedUserID = resolvedID

			// Look up channel locale from DB.
			if c.store != nil {
				if locale, err := c.store.GetChannelLocale(ctx, "telegram", msg.UserID); err == nil {
					msg.Locale = locale
				}
			}
		}

		// Shared location or venue: structured attachment + localized text line
		// (TelegramVoicePrefix precedent) so the LLM sees it and history keeps it.
		// Telegram location shares carry no caption — the append branch is future-proofing.
		if hasLocation {
			if loc := locationFromMessage(update.Message); loc != nil {
				msg.Locations = []channel.LocationAttachment{*loc}
				line := formatLocationText(i18n.ResolveLocale(msg.Locale, msg.LanguageCode), loc)
				if msg.Text != "" {
					msg.Text = msg.Text + "\n" + line
				} else {
					msg.Text = line
				}
				c.logger.Debug("location received",
					zap.String("chat_id", chatID),
					zap.Bool("live", loc.Live),
					zap.Bool("venue", loc.Title != ""))
			}
		}

		// Download photo if present.
		if hasPhoto {
			photo := update.Message.Photo[len(update.Message.Photo)-1] // largest size
			data, err := c.downloadFile(ctx, photo.FileID)
			if err != nil {
				c.logger.Error("failed to download photo", zap.Error(err), zap.String("chat_id", chatID))
			} else {
				msg.Images = []channel.ImageAttachment{
					{Data: data, MediaType: "image/jpeg"},
				}
			}
		}

		// Download document if present (PDF, text files).
		if hasDocument {
			doc := update.Message.Document
			if c.isSupportedDocument(doc.MimeType) {
				if doc.FileSize > 30*1024*1024 {
					c.logger.Warn("document too large, skipping",
						zap.String("filename", doc.FileName),
						zap.Int64("size", doc.FileSize),
						zap.String("chat_id", chatID))
				} else {
					data, err := c.downloadFile(ctx, doc.FileID)
					if err != nil {
						c.logger.Error("failed to download document", zap.Error(err),
							zap.String("filename", doc.FileName), zap.String("chat_id", chatID))
					} else {
						msg.Documents = []channel.DocumentAttachment{
							{Data: data, MimeType: doc.MimeType, Filename: doc.FileName},
						}
					}
				}
			} else {
				c.logger.Warn("unsupported document type, skipping",
					zap.String("mime_type", doc.MimeType),
					zap.String("filename", doc.FileName),
					zap.String("chat_id", chatID))
			}
		}

		// Download and transcribe voice/audio if present.
		if hasVoice || hasAudio {
			var fileID string
			var duration int
			if hasVoice {
				fileID = update.Message.Voice.FileID
				duration = update.Message.Voice.Duration
			} else {
				fileID = update.Message.Audio.FileID
				duration = update.Message.Audio.Duration
			}

			if c.transcriber != nil {
				data, err := c.downloadFile(ctx, fileID)
				if err != nil {
					c.logger.Error("failed to download voice message", zap.Error(err), zap.String("chat_id", chatID))
				} else {
					transcribed, err := c.transcriber.Transcribe(ctx, data, "ogg")
					if err != nil {
						c.logger.Error("failed to transcribe voice message", zap.Error(err), zap.String("chat_id", chatID))
					} else if transcribed != "" {
						localeCtx := i18n.WithLocale(ctx, i18n.ResolveLocale(msg.Locale, msg.LanguageCode))
						prefix := i18n.T(localeCtx, "TelegramVoicePrefix")
						if msg.Text != "" {
							msg.Text = msg.Text + "\n" + prefix + transcribed
						} else {
							msg.Text = prefix + transcribed
						}
					}
				}
				msg.Audio = []channel.AudioAttachment{
					{Format: "ogg", Duration: duration},
				}
			} else {
				c.logger.Debug("voice message received but no transcriber configured",
					zap.String("chat_id", chatID), zap.Int("duration", duration))
			}
		}

		// A voice/audio note whose transcription failed or came back empty
		// must not produce an empty LLM turn (providers reject empty user
		// messages). Inject a localized fallback line so the model can tell
		// the user it could not hear the note.
		if (hasVoice || hasAudio) && msg.Text == "" {
			localeCtx := i18n.WithLocale(ctx, i18n.ResolveLocale(msg.Locale, msg.LanguageCode))
			msg.Text = i18n.T(localeCtx, "TelegramVoicePrefix") + i18n.T(localeCtx, "TelegramVoiceUnavailable")
		}

		// Skip messages with no usable content (e.g. unsupported GIF/animation).
		if msg.Text == "" && len(msg.Images) == 0 && len(msg.Documents) == 0 &&
			len(msg.Audio) == 0 && len(msg.Locations) == 0 {
			c.logger.Debug("skipping message with no content",
				zap.String("chat_id", chatID), zap.Int64("user_id", userID))
			return
		}

		// Rate limit check.
		if c.rateLimiter != nil && !c.rateLimiter.Allow(chatID) {
			c.logger.Warn("rate limit exceeded", zap.String("chat_id", chatID), zap.Int64("user_id", userID))
			localeCtx := i18n.WithLocale(ctx, i18n.ResolveLocale(msg.Locale, msg.LanguageCode))
			c.sendSingleMessage(update.Message.Chat.ID, i18n.T(localeCtx, "TelegramRateLimited"), 0)
			return
		}

		debounce.add(msg)
	}

	c.bot.RegisterHandlerMatchFunc(func(*models.Update) bool { return true },
		func(_ context.Context, _ *bot.Bot, update *models.Update) { dispatch(*update) })

	// Long-polling loop owned by the library (Bot API 10.3 models decode all
	// update types natively, including message_reaction).
	c.bot.Start(ctx)

	c.logger.Info("shutdown: flushing pending debounced messages")
	debounce.flushAll()
	c.logger.Info("shutdown: waiting for in-flight message processing to finish")
	c.wg.Wait()
	c.logger.Info("shutdown: all message processing complete")
	return ctx.Err()
}

func (c *Channel) handleClear(ctx context.Context, tgChatID int64, chatID string) {
	// Use default locale context (will be resolved from DB if needed).
	localeCtx := ctx
	if c.store != nil {
		if locale, err := c.store.GetChannelLocaleByChatID(localeCtx, chatID); err == nil {
			localeCtx = i18n.WithLocale(ctx, i18n.ResolveLocale(locale, ""))
		}
	}
	if err := c.clearFn(ctx, chatID); err != nil {
		c.logger.Error("failed to clear history", zap.Error(err), zap.String("chat_id", chatID))
		_, _ = c.bot.SendMessage(ctx, &bot.SendMessageParams{ //nolint:errcheck // best-effort notify
			ChatID: tgChatID,
			Text:   i18n.T(localeCtx, "TelegramHistoryClearFailed"),
		})
		return
	}
	// Natively delete the bot's own messages from the last 48 hours.
	c.deleteBotMessages(tgChatID)
	_, _ = c.bot.SendMessage(ctx, &bot.SendMessageParams{ //nolint:errcheck // best-effort notify
		ChatID: tgChatID,
		Text:   i18n.T(localeCtx, "TelegramHistoryCleared"),
	})
}

// SendMessage sends a proactive message to a chat (reminders, agent job
// results). These are rare, attention-worthy pings, so they carry the 🎉
// effect. Implements channel.MessageSender.
func (c *Channel) SendMessage(_ context.Context, chatID, text string) error {
	tgChatID, err := strconv.ParseInt(chatID, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid chat ID: %w", err)
	}
	for i, chunk := range telegramHTMLChunks(text) {
		rt := 0
		if i == 0 {
			// replyTo stays 0 for proactive messages
			rt = 0
		}
		_, _ = c.sendHTML(tgChatID, chunk, rt, nil, effectParty) //nolint:errcheck // best-effort proactive push
	}
	return nil
}

// sendResponse splits long messages into chunks and sends each as HTML.
// replyTo is the message ID to reply to (0 = no reply).
func (c *Channel) sendResponse(chatID int64, text string, replyTo int) {
	for i, chunk := range telegramHTMLChunks(text) {
		// Only reply-to the first chunk.
		rt := 0
		if i == 0 {
			rt = replyTo
		}
		_, _ = c.sendHTML(chatID, chunk, rt, nil, "") //nolint:errcheck // sendHTML logs failures
	}
}

// sendSingleMessage sends a plain (non-markdown) message, HTML-escaped.
// replyTo is the message ID to reply to (0 = no reply).
func (c *Channel) sendSingleMessage(chatID int64, text string, replyTo int) {
	_, _ = c.sendHTML(chatID, escapeHTML(text), replyTo, nil, "") //nolint:errcheck // sendHTML logs failures
}

// StartStream sends an initial message and returns edit/done functions for streaming.
// Implements channel.StreamingSender.
func (c *Channel) StartStream(_ context.Context, chatID string, replyTo int) (func(string), func(string), error) {
	tgChatID, err := strconv.ParseInt(chatID, 10, 64)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid chat ID: %w", err)
	}

	// Reuse the status message if it was consumed by stream_start AND the task was quick.
	// For long tasks (>30s), keep the status message as a separate log and send a fresh response.
	var msgID int
	if entry, ok := c.statusMsgs.get(chatID); ok && entry.isConsumed() && !entry.isLongTask() {
		msgID = entry.getMsgID()
		c.statusMsgs.remove(chatID)
		// Edit status message to streaming placeholder.
		_, _ = c.bot.EditMessageText(context.Background(), &bot.EditMessageTextParams{ //nolint:errcheck // best-effort placeholder
			ChatID:    tgChatID,
			MessageID: msgID,
			Text:      "...",
		})
	} else {
		// For long tasks: finalize the status message with total time, then send fresh response.
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
	var lastEdit time.Time

	editFn := func(text string) {
		if time.Since(lastEdit) < 1500*time.Millisecond {
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
		lastEdit = time.Now()
	}

	doneFn := func(text string) {
		c.editHTML(tgChatID, msgID, toTelegramHTML(text), "")
		c.sentMsgs.record(tgChatID, msgID, text)
	}

	return editFn, doneFn, nil
}

// NotifyStatus sends a live status message to the chat, or updates it in-place.
// Implements channel.StatusNotifier.
func (c *Channel) NotifyStatus(_ context.Context, chatID string, event channel.StatusEvent) error {
	tgChatID, parseErr := strconv.ParseInt(chatID, 10, 64)
	if parseErr != nil {
		return nil //nolint:nilerr // non-numeric chatIDs are silently ignored (not a Telegram chat)
	}

	// Handle special event types before formatting.

	// stream_start: atomically mark the status message as consumed (streaming will take over).
	if event.Type == "stream_start" {
		c.statusMsgs.getAndMarkConsumed(chatID)
		return nil
	}

	// skip_bookmark: signal that remember was used, no bookmark button needed.
	if event.Type == "skip_bookmark" {
		if entry, ok := c.statusMsgs.get(chatID); ok {
			entry.mu.Lock()
			entry.skipBookmark = true
			entry.mu.Unlock()
		}
		return nil
	}

	line, replaces := formatStatusLine(event)
	if line == "" {
		return nil // unknown event type
	}

	// Get or create status entry.
	entry, exists := c.statusMsgs.get(chatID)
	if !exists || entry.msgID == 0 {
		// Send initial status message (entry may be a pre-created placeholder with replyTo).
		p := &bot.SendMessageParams{ChatID: tgChatID, Text: line}
		// Preserve reply threading from the pre-registered replyTo.
		if entry != nil && entry.replyTo > 0 {
			p.ReplyParameters = &models.ReplyParameters{MessageID: entry.replyTo}
		}
		sent, sendErr := c.bot.SendMessage(context.Background(), p)
		if sendErr != nil {
			c.logger.Debug("failed to send status message", zap.Error(sendErr))
			return nil
		}
		entry = c.statusMsgs.create(chatID, tgChatID, sent.ID)
		entry.addLine(line)
		return nil
	}

	// Update existing status message.
	if replaces {
		entry.updateLastLine(line)
	} else {
		entry.addLine(line)
	}

	// Rate-limited edit.
	if entry.canEdit() {
		_, editErr := c.bot.EditMessageText(context.Background(), &bot.EditMessageTextParams{
			ChatID:    tgChatID,
			MessageID: entry.msgID,
			Text:      entry.renderText(),
		})
		if editErr != nil {
			c.logger.Debug("status edit failed", zap.Error(editErr))
		}
		entry.markEdited()
	}

	return nil
}

// deleteStatusMessage removes the live status message for a chat.
// If the message was consumed by streaming, this is a no-op.
// Runs the actual delete in a goroutine to avoid blocking the response.
func (c *Channel) deleteStatusMessage(chatID string, tgChatID int64) {
	entry, ok := c.statusMsgs.get(chatID)
	if !ok {
		return
	}
	c.statusMsgs.remove(chatID)

	if entry.isConsumed() {
		return // streaming took ownership
	}

	msgID := entry.getMsgID()
	sentAt := entry.sentAt

	// Delete in background to avoid blocking the actual response.
	go func() {
		// Ensure the message was visible for at least minStatusDisplay.
		if elapsed := time.Since(sentAt); elapsed < minStatusDisplay {
			time.Sleep(minStatusDisplay - elapsed)
		}
		if _, err := c.bot.DeleteMessage(context.Background(), &bot.DeleteMessageParams{
			ChatID:    tgChatID,
			MessageID: msgID,
		}); err != nil {
			c.logger.Debug("failed to delete status message", zap.Error(err))
		}
	}()
}

// finalizeStatusMessage updates the status message with total elapsed time
// and removes it from tracking. Used for long tasks where the status message
// is kept as a separate log alongside the response.
func (c *Channel) finalizeStatusMessage(chatID string, entry *statusEntry) {
	elapsed := time.Since(entry.sentAt)
	entry.addLine(fmt.Sprintf("\n✅ Done in %s", elapsed.Round(time.Second)))

	if _, err := c.bot.EditMessageText(context.Background(), &bot.EditMessageTextParams{
		ChatID:    entry.tgChatID,
		MessageID: entry.getMsgID(),
		Text:      entry.renderText(),
	}); err != nil {
		c.logger.Debug("failed to finalize status message", zap.Error(err))
	}
	c.statusMsgs.remove(chatID)
}

// isSupportedDocument returns true if the MIME type is accepted for document forwarding.
func (c *Channel) isSupportedDocument(mimeType string) bool {
	switch mimeType {
	case "application/pdf", "text/plain", "text/csv", "text/markdown", "text/html", "application/json":
		return true
	}
	return false
}

// downloadFile fetches file bytes from Telegram servers.
func (c *Channel) downloadFile(ctx context.Context, fileID string) ([]byte, error) {
	f, err := c.bot.GetFile(ctx, &bot.GetFileParams{FileID: fileID})
	if err != nil {
		return nil, fmt.Errorf("getting file: %w", err)
	}
	url := c.bot.FileDownloadLink(f)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading file: %w", sanitizeTGError(err))
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading file body: %w", err)
	}
	return data, nil
}

// keepTyping sends the "typing..." action every 4 seconds until ctx is canceled.
func (c *Channel) keepTyping(ctx context.Context, chatID int64) {
	typing := func() {
		_, _ = c.bot.SendChatAction(ctx, &bot.SendChatActionParams{ //nolint:errcheck // best-effort indicator
			ChatID: chatID,
			Action: models.ChatActionTyping,
		})
	}
	typing()

	ticker := time.NewTicker(4 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			typing()
		}
	}
}

// healthMonitor periodically verifies Telegram API connectivity.
func (c *Channel) healthMonitor(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	consecutiveFailures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := c.bot.GetMe(ctx); err != nil {
				consecutiveFailures++
				c.logger.Error("telegram health check failed",
					zap.Error(err),
					zap.Int("consecutive_failures", consecutiveFailures))
			} else if consecutiveFailures > 0 {
				c.logger.Info("telegram health check recovered",
					zap.Int("after_failures", consecutiveFailures))
				consecutiveFailures = 0
			}
		}
	}
}

func (c *Channel) isAllowed(userID int64) bool {
	if len(c.allowedIDs) == 0 {
		return true
	}
	_, ok := c.allowedIDs[userID]
	return ok
}

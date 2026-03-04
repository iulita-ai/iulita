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

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"go.uber.org/zap"

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
	bot            *tgbotapi.BotAPI
	instanceID     string // channel instance slug (e.g., "tg-config")
	allowedIDs     map[int64]struct{}
	clearFn        ClearFunc
	commands       map[string]CommandFunc
	commandOrder   []tgbotapi.BotCommand
	debounceWindow time.Duration
	rateLimiter    *ratelimit.Limiter    // nil = no rate limiting
	userResolver   channel.UserResolver  // nil = no user resolution (backward compat)
	store          storage.Repository    // nil = no locale lookup
	transcriber    TranscriptionProvider // nil = voice messages ignored
	prompts        *promptState          // interactive skill prompts
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
	c.commandOrder = append(c.commandOrder, tgbotapi.BotCommand{
		Command:     strings.TrimPrefix(name, "/"),
		Description: description,
	})
}

// New creates a new Telegram channel.
func New(token string, allowedIDs []int64, clearFn ClearFunc, debounceWindow time.Duration, httpClient *http.Client, logger *zap.Logger) (*Channel, error) {
	var bot *tgbotapi.BotAPI
	var err error
	if httpClient != nil {
		bot, err = tgbotapi.NewBotAPIWithClient(token, tgbotapi.APIEndpoint, httpClient)
	} else {
		bot, err = tgbotapi.NewBotAPI(token)
	}
	if err != nil {
		return nil, fmt.Errorf("creating telegram bot: %w", err)
	}

	allowed := make(map[int64]struct{}, len(allowedIDs))
	for _, id := range allowedIDs {
		allowed[id] = struct{}{}
	}

	logger.Info("telegram bot authorized", zap.String("username", bot.Self.UserName))

	return &Channel{
		bot:            bot,
		allowedIDs:     allowed,
		clearFn:        clearFn,
		commands:       make(map[string]CommandFunc),
		debounceWindow: debounceWindow,
		prompts:        newPromptState(),
		logger:         logger,
	}, nil
}

func (c *Channel) Start(ctx context.Context, handler channel.MessageHandler) error {
	// Register commands in Telegram's menu (the "/" button).
	if len(c.commandOrder) > 0 {
		allCmds := append([]tgbotapi.BotCommand{
			{Command: "clear", Description: i18n.Tl(i18n.ResolveLocale("", "en"), "TelegramClearCommand")},
		}, c.commandOrder...)
		cmdCfg := tgbotapi.NewSetMyCommands(allCmds...)
		if _, err := c.bot.Request(cmdCfg); err != nil {
			c.logger.Error("failed to set bot commands menu", zap.Error(err))
		} else {
			c.logger.Info("registered bot commands in Telegram menu", zap.Int("count", len(allCmds)))
		}
	}

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 30

	updates := c.bot.GetUpdatesChan(u)

	// processMsg handles a (possibly debounced/merged) message.
	processMsg := func(msg channel.IncomingMessage) {
		tgChatID, _ := strconv.ParseInt(msg.ChatID, 10, 64)

		handlerCtx := context.WithoutCancel(ctx)
		typingCtx, stopTyping := context.WithCancel(handlerCtx)
		go c.keepTyping(typingCtx, tgChatID)

		c.wg.Add(1)
		response, err := handler(handlerCtx, msg)
		stopTyping()
		c.wg.Done()
		if err != nil {
			c.logger.Error("handler error", zap.Error(err), zap.String("chat_id", msg.ChatID))
			localeCtx := i18n.WithLocale(handlerCtx, i18n.ResolveLocale(msg.Locale, msg.LanguageCode))
			response = i18n.T(localeCtx, "TelegramErrorResponse")
		}

		c.sendResponse(tgChatID, response, msg.MessageID)
	}

	debounce := newDebouncer(c.debounceWindow, processMsg)
	if c.debounceWindow > 0 {
		c.logger.Info("message debouncing enabled", zap.Duration("window", c.debounceWindow))
	}

	// Health monitor: periodic bot.GetMe() check.
	go c.healthMonitor(ctx)

	for {
		select {
		case <-ctx.Done():
			c.bot.StopReceivingUpdates()
			c.logger.Info("shutdown: flushing pending debounced messages")
			debounce.flushAll()
			c.logger.Info("shutdown: waiting for in-flight message processing to finish")
			c.wg.Wait()
			c.logger.Info("shutdown: all message processing complete")
			return ctx.Err()

		case update := <-updates:
			// Handle inline keyboard callback queries (interactive prompts).
			if update.CallbackQuery != nil && update.CallbackQuery.From != nil {
				if c.isAllowed(update.CallbackQuery.From.ID) {
					if c.prompts.HandleCallback(c.bot, update.CallbackQuery) {
						continue
					}
				}
			}

			if update.Message == nil {
				continue
			}

			hasText := update.Message.Text != ""
			hasPhoto := len(update.Message.Photo) > 0
			hasDocument := update.Message.Document != nil
			hasVoice := update.Message.Voice != nil
			hasAudio := update.Message.Audio != nil
			if !hasText && !hasPhoto && !hasDocument && !hasVoice && !hasAudio {
				continue
			}

			userID := update.Message.From.ID
			if !c.isAllowed(userID) {
				c.logger.Warn("unauthorized user", zap.Int64("user_id", userID))
				continue
			}

			chatID := strconv.FormatInt(update.Message.Chat.ID, 10)

			// Handle /clear command.
			if update.Message.Text == "/clear" {
				c.handleClear(ctx, update.Message.Chat.ID, chatID)
				continue
			}

			// Handle registered commands.
			if strings.HasPrefix(update.Message.Text, "/") {
				cmd := strings.Fields(update.Message.Text)[0]
				if fn, ok := c.commands[cmd]; ok {
					resp := fn(ctx, chatID)
					if resp != "" {
						c.sendResponse(update.Message.Chat.ID, resp, 0)
					}
					continue
				}
			}

			// Check if this text should be routed to a pending interactive prompt.
			if hasText && c.prompts.HandleText(update.Message.Chat.ID, update.Message.Text) {
				continue
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
				UserName:          update.Message.From.UserName,
				Text:              text,
				LanguageCode:      update.Message.From.LanguageCode,
				MessageID:         update.Message.MessageID,
				Caps:              channel.CapStreaming | channel.CapMarkdown | channel.CapTyping | channel.CapButtons,
			}

			// Resolve iulita user from channel binding.
			if c.userResolver != nil {
				resolvedID, err := c.userResolver.ResolveUser(ctx, "telegram", msg.UserID, msg.UserName, chatID)
				if err != nil {
					c.logger.Warn("user resolution failed", zap.Error(err), zap.String("user_id", msg.UserID))
					localeCtx := i18n.WithLocale(ctx, i18n.ResolveLocale("", msg.LanguageCode))
					c.sendSingleMessage(update.Message.Chat.ID, i18n.T(localeCtx, "TelegramRegistrationNotAllowed"), 0)
					continue
				}
				msg.ResolvedUserID = resolvedID

				// Look up channel locale from DB.
				if c.store != nil {
					if locale, err := c.store.GetChannelLocale(ctx, "telegram", msg.UserID); err == nil {
						msg.Locale = locale
					}
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
							zap.Int("size", doc.FileSize),
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

			// Skip messages with no usable content (e.g. unsupported GIF/animation).
			if msg.Text == "" && len(msg.Images) == 0 && len(msg.Documents) == 0 {
				c.logger.Debug("skipping message with no content",
					zap.String("chat_id", chatID), zap.Int64("user_id", userID))
				continue
			}

			// Rate limit check.
			if c.rateLimiter != nil && !c.rateLimiter.Allow(chatID) {
				c.logger.Warn("rate limit exceeded", zap.String("chat_id", chatID), zap.Int64("user_id", userID))
				localeCtx := i18n.WithLocale(ctx, i18n.ResolveLocale(msg.Locale, msg.LanguageCode))
				c.sendSingleMessage(update.Message.Chat.ID, i18n.T(localeCtx, "TelegramRateLimited"), 0)
				continue
			}

			debounce.add(msg)
		}
	}
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
		reply := tgbotapi.NewMessage(tgChatID, i18n.T(localeCtx, "TelegramHistoryClearFailed"))
		c.bot.Send(reply)
		return
	}
	reply := tgbotapi.NewMessage(tgChatID, i18n.T(localeCtx, "TelegramHistoryCleared"))
	c.bot.Send(reply)
}

// SendMessage sends a proactive message to a chat. Implements channel.MessageSender.
func (c *Channel) SendMessage(_ context.Context, chatID string, text string) error {
	tgChatID, err := strconv.ParseInt(chatID, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid chat ID: %w", err)
	}
	c.sendResponse(tgChatID, text, 0)
	return nil
}

// sendResponse splits long messages into chunks and sends each with Markdown fallback.
// replyTo is the message ID to reply to (0 = no reply).
func (c *Channel) sendResponse(chatID int64, text string, replyTo int) {
	chunks := splitMessage(text, maxMessageLen)
	for i, chunk := range chunks {
		// Only reply-to the first chunk.
		rt := 0
		if i == 0 {
			rt = replyTo
		}
		c.sendSingleMessage(chatID, chunk, rt)
	}
}

// sendSingleMessage sends a single message with Markdown, falling back to plain text.
// replyTo is the message ID to reply to (0 = no reply).
func (c *Channel) sendSingleMessage(chatID int64, text string, replyTo int) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = tgbotapi.ModeMarkdown
	if replyTo > 0 {
		msg.ReplyToMessageID = replyTo
	}
	if _, err := c.bot.Send(msg); err != nil {
		c.logger.Debug("markdown send failed, retrying as plain text", zap.Error(err))
		msg.ParseMode = ""
		if _, err := c.bot.Send(msg); err != nil {
			c.logger.Error("failed to send message",
				zap.Error(err),
				zap.Int64("chat_id", chatID),
			)
		}
	}
}

// StartStream sends an initial message and returns edit/done functions for streaming.
// Implements channel.StreamingSender.
func (c *Channel) StartStream(_ context.Context, chatID string, replyTo int) (func(string), func(string), error) {
	tgChatID, err := strconv.ParseInt(chatID, 10, 64)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid chat ID: %w", err)
	}

	msg := tgbotapi.NewMessage(tgChatID, "...")
	if replyTo > 0 {
		msg.ReplyToMessageID = replyTo
	}
	sent, err := c.bot.Send(msg)
	if err != nil {
		return nil, nil, fmt.Errorf("sending initial stream message: %w", err)
	}

	msgID := sent.MessageID
	var lastEdit time.Time

	editFn := func(text string) {
		if time.Since(lastEdit) < 1500*time.Millisecond {
			return // coalesce edits
		}
		edit := tgbotapi.NewEditMessageText(tgChatID, msgID, text)
		if _, err := c.bot.Send(edit); err != nil {
			c.logger.Debug("stream edit failed", zap.Error(err))
		}
		lastEdit = time.Now()
	}

	doneFn := func(text string) {
		edit := tgbotapi.NewEditMessageText(tgChatID, msgID, text)
		edit.ParseMode = tgbotapi.ModeMarkdown
		if _, err := c.bot.Send(edit); err != nil {
			// Retry without markdown.
			edit.ParseMode = ""
			c.bot.Send(edit)
		}
	}

	return editFn, doneFn, nil
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
	url, err := c.bot.GetFileDirectURL(fileID)
	if err != nil {
		return nil, fmt.Errorf("getting file URL: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := c.bot.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading file body: %w", err)
	}
	return data, nil
}

// keepTyping sends the "typing..." action every 4 seconds until ctx is cancelled.
func (c *Channel) keepTyping(ctx context.Context, chatID int64) {
	typing := tgbotapi.NewChatAction(chatID, tgbotapi.ChatTyping)
	c.bot.Send(typing) // send immediately

	ticker := time.NewTicker(4 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.bot.Send(typing)
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
			if _, err := c.bot.GetMe(); err != nil {
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

// Package discord implements channel.InputChannel for Discord bots.
package discord

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/bwmarrin/discordgo"
	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/ratelimit"
)

const maxMessageLen = 2000

// ClearFunc is called to clear conversation history for a chat.
type ClearFunc func(ctx context.Context, chatID string) error

// Channel implements channel.InputChannel for Discord.
type Channel struct {
	session           *discordgo.Session
	instanceID        string
	allowedChannelIDs map[string]struct{}
	clearFn           ClearFunc
	rateLimiter       *ratelimit.Limiter
	userResolver      channel.UserResolver
	logger            *zap.Logger
	wg                sync.WaitGroup
}

// New creates a new Discord channel.
func New(token string, allowedChannelIDs []string, clearFn ClearFunc, logger *zap.Logger) (*Channel, error) {
	session, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, fmt.Errorf("creating discord session: %w", err)
	}

	session.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages | discordgo.IntentMessageContent

	allowed := make(map[string]struct{}, len(allowedChannelIDs))
	for _, id := range allowedChannelIDs {
		allowed[id] = struct{}{}
	}

	return &Channel{
		session:           session,
		allowedChannelIDs: allowed,
		clearFn:           clearFn,
		logger:            logger,
	}, nil
}

// SetInstanceID sets the channel instance ID.
func (c *Channel) SetInstanceID(id string) {
	c.instanceID = id
}

// SetRateLimiter attaches a per-chat rate limiter.
func (c *Channel) SetRateLimiter(rl *ratelimit.Limiter) {
	c.rateLimiter = rl
}

// SetUserResolver attaches a user resolver.
func (c *Channel) SetUserResolver(ur channel.UserResolver) {
	c.userResolver = ur
}

// Start connects to Discord and processes messages until ctx is cancelled.
func (c *Channel) Start(ctx context.Context, handler channel.MessageHandler) error {
	c.session.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		// Ignore bot's own messages.
		if m.Author.ID == s.State.User.ID {
			return
		}

		// Check allowed channels (if configured).
		if len(c.allowedChannelIDs) > 0 {
			if _, ok := c.allowedChannelIDs[m.ChannelID]; !ok {
				return
			}
		}

		chatID := m.ChannelID

		// Handle /clear command.
		if strings.TrimSpace(m.Content) == "/clear" {
			if c.clearFn != nil {
				if err := c.clearFn(ctx, chatID); err != nil {
					c.logger.Error("discord: failed to clear history", zap.Error(err))
					s.ChannelMessageSend(chatID, "Failed to clear history.")
				} else {
					s.ChannelMessageSend(chatID, "History cleared.")
				}
			}
			return
		}

		if m.Content == "" {
			return
		}

		// Rate limit check.
		if c.rateLimiter != nil && !c.rateLimiter.Allow(chatID) {
			s.ChannelMessageSend(chatID, "Too many messages. Please wait a moment.")
			return
		}

		msg := channel.IncomingMessage{
			ChatID:            chatID,
			UserID:            m.Author.ID,
			ChannelInstanceID: c.instanceID,
			UserName:          m.Author.Username,
			Text:              m.Content,
		}

		// Resolve user.
		if c.userResolver != nil {
			resolvedID, err := c.userResolver.ResolveUser(ctx, "discord", msg.UserID, msg.UserName, chatID)
			if err != nil {
				c.logger.Warn("discord: user resolution failed", zap.Error(err))
				s.ChannelMessageSend(chatID, "Registration is not allowed. Contact admin.")
				return
			}
			msg.ResolvedUserID = resolvedID
		}

		// Handle attachments.
		for _, att := range m.Attachments {
			if strings.HasPrefix(att.ContentType, "image/") {
				// Images would need downloading — skip for now, just note in text.
				msg.Text += fmt.Sprintf("\n[Image: %s]", att.URL)
			}
		}

		c.wg.Add(1)
		go func() {
			defer c.wg.Done()

			// Show typing indicator.
			s.ChannelTyping(chatID)

			handlerCtx := context.WithoutCancel(ctx)
			response, err := handler(handlerCtx, msg)
			if err != nil {
				c.logger.Error("discord: handler error", zap.Error(err), zap.String("chat_id", chatID))
				response = "Sorry, something went wrong."
			}

			c.sendResponse(chatID, response)
		}()
	})

	if err := c.session.Open(); err != nil {
		return fmt.Errorf("opening discord connection: %w", err)
	}
	c.logger.Info("discord bot connected", zap.String("instance_id", c.instanceID))

	<-ctx.Done()

	c.logger.Info("discord: shutting down, waiting for in-flight messages")
	c.wg.Wait()

	if err := c.session.Close(); err != nil {
		c.logger.Error("discord: error closing session", zap.Error(err))
	}
	return ctx.Err()
}

// SendMessage sends a proactive message to a Discord channel.
func (c *Channel) SendMessage(_ context.Context, chatID string, text string) error {
	c.sendResponse(chatID, text)
	return nil
}

// StartStream sends an initial message and returns edit/done functions for streaming.
func (c *Channel) StartStream(_ context.Context, chatID string, _ int) (func(string), func(string), error) {
	sent, err := c.session.ChannelMessageSend(chatID, "...")
	if err != nil {
		return nil, nil, fmt.Errorf("sending initial discord message: %w", err)
	}

	editFn := func(text string) {
		if len(text) > maxMessageLen {
			text = text[:maxMessageLen]
		}
		c.session.ChannelMessageEdit(chatID, sent.ID, text)
	}

	doneFn := func(text string) {
		if len(text) > maxMessageLen {
			// Split into multiple messages for final.
			chunks := splitMessage(text, maxMessageLen)
			if len(chunks) > 0 {
				c.session.ChannelMessageEdit(chatID, sent.ID, chunks[0])
				for _, chunk := range chunks[1:] {
					c.session.ChannelMessageSend(chatID, chunk)
				}
			}
		} else {
			c.session.ChannelMessageEdit(chatID, sent.ID, text)
		}
	}

	return editFn, doneFn, nil
}

// sendResponse splits long messages and sends each chunk.
func (c *Channel) sendResponse(chatID, text string) {
	chunks := splitMessage(text, maxMessageLen)
	for _, chunk := range chunks {
		if _, err := c.session.ChannelMessageSend(chatID, chunk); err != nil {
			c.logger.Error("discord: failed to send message",
				zap.Error(err), zap.String("chat_id", chatID))
		}
	}
}

// splitMessage splits text into chunks of at most maxLen characters.
func splitMessage(text string, maxLen int) []string {
	if len(text) <= maxLen {
		return []string{text}
	}

	var chunks []string
	for len(text) > 0 {
		if len(text) <= maxLen {
			chunks = append(chunks, text)
			break
		}

		// Try to split at last newline within maxLen.
		idx := strings.LastIndex(text[:maxLen], "\n")
		if idx <= 0 {
			idx = maxLen
		}

		chunks = append(chunks, text[:idx])
		text = text[idx:]
		if len(text) > 0 && text[0] == '\n' {
			text = text[1:]
		}
	}
	return chunks
}

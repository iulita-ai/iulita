package telegram

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/domain"
)

// ErrTopicPaused indicates that Iulita delivery is paused for a topic.
var ErrTopicPaused = channel.ErrDeliveryPaused

type routeStore interface {
	PutTelegramRoute(context.Context, *domain.TelegramConversationRoute) error
	GetTelegramRoute(context.Context, string) (*domain.TelegramConversationRoute, error)
}

func (c *Channel) routeKey(chat int64, thread int) string {
	if thread == 0 {
		return strconv.FormatInt(chat, 10)
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s/%d/%d", c.instanceID, chat, thread)))
	return fmt.Sprintf("tgconv:%x", sum[:16])
}
func (c *Channel) conversationFor(ctx context.Context, m *models.Message) (string, error) {
	key := c.routeKey(m.Chat.ID, m.MessageThreadID)
	if m.MessageThreadID == 0 {
		if s, ok := c.store.(routeStore); ok {
			r, e := s.GetTelegramRoute(ctx, key)
			if e != nil {
				return "", e
			}
			if r != nil && r.InstanceID != c.instanceID {
				sum := sha256.Sum256([]byte(fmt.Sprintf("%s/%d/0", c.instanceID, m.Chat.ID)))
				key = fmt.Sprintf("tgconv:%x", sum[:16])
			}
		}
	}
	return key, nil
}
func (c *Channel) persistRoute(ctx context.Context, key string, m *models.Message, owner string) error {
	s, ok := c.store.(routeStore)
	if !ok {
		if strings.HasPrefix(key, "tgconv:") {
			return fmt.Errorf("telegram route store unavailable")
		}
		return nil
	}
	// Shared chats have a route access scope, not the last sender as owner.
	// This metadata cannot grant private access (Private is always false).
	if m.Chat.Type != models.ChatTypePrivate {
		owner = fmt.Sprintf("telegram-chat:%d", m.Chat.ID)
	}
	botID := c.botID
	if old, e := s.GetTelegramRoute(ctx, key); e != nil {
		return e
	} else if old != nil && old.BotID == 0 {
		// Legacy context remains usable, but unknown provenance must never be
		// silently rebound to the currently configured bot for destructive sync.
		botID = 0
	}
	return s.PutTelegramRoute(ctx, &domain.TelegramConversationRoute{BotID: botID, ConversationID: key, InstanceID: c.instanceID, ChatID: m.Chat.ID, ThreadID: m.MessageThreadID, OwnerID: owner, Private: m.Chat.Type == models.ChatTypePrivate && m.From != nil && m.From.ID == m.Chat.ID})
}
func (c *Channel) resolveTargetForLifecycle(ctx context.Context, key string) (channel.TelegramTarget, error) {
	if s, ok := c.store.(routeStore); ok {
		r, e := s.GetTelegramRoute(ctx, key)
		if e != nil {
			return channel.TelegramTarget{}, e
		}
		if r != nil {
			if r.InstanceID != c.instanceID {
				return channel.TelegramTarget{}, fmt.Errorf("telegram instance mismatch")
			}
			if r.BotID > 0 && c.botID > 0 && r.BotID != c.botID {
				return channel.TelegramTarget{}, fmt.Errorf("telegram bot identity mismatch")
			}
			return channel.TelegramTarget{BotID: r.BotID, InstanceID: r.InstanceID, ChatID: r.ChatID, ThreadID: r.ThreadID, OwnerID: r.OwnerID, Private: r.Private}, nil
		}
	}
	if strings.HasPrefix(key, "tgconv:") {
		return channel.TelegramTarget{}, fmt.Errorf("unknown Telegram conversation")
	}
	id, e := strconv.ParseInt(key, 10, 64)
	if e != nil || id == 0 {
		return channel.TelegramTarget{}, fmt.Errorf("invalid Telegram target")
	}
	return channel.TelegramTarget{InstanceID: c.instanceID, ChatID: id}, nil
}
func (c *Channel) turnSender(key string) int64 {
	c.confirmSendersMu.Lock()
	defer c.confirmSendersMu.Unlock()
	return c.confirmSenders[key]
}

// TopicLink only returns links belonging to this bot and the requested owner.
// A username or link is never accepted as an authorization credential.
func (c *Channel) TopicLink(ctx context.Context, key, owner string) (string, error) {
	t, e := c.resolveTarget(ctx, key)
	if e != nil {
		return "", e
	}
	if !t.Private || t.OwnerID != owner || owner == "" || t.ThreadID == 0 {
		return "", fmt.Errorf("private topic unavailable")
	}
	if c.username == "" {
		return "", fmt.Errorf("bot username unavailable")
	}
	return fmt.Sprintf("https://t.me/%s/%d", c.linkUsername(), t.ThreadID), nil
}

// Username returns the canonical verified bot username.
func (c *Channel) Username() string { return c.username }

func (c *Channel) resolveTarget(ctx context.Context, key string) (channel.TelegramTarget, error) {
	t, e := c.resolveTargetForLifecycle(ctx, key)
	if e != nil {
		return t, e
	}
	if store, ok := c.store.(topicStore); ok {
		state, err := store.GetTelegramTopicState(ctx, key)
		if err != nil {
			return channel.TelegramTarget{}, err
		}
		if state != nil && (state.Status == "paused" || state.Status == "closed") {
			return channel.TelegramTarget{}, ErrTopicPaused
		}
		if state != nil && (state.Status == "deleted" || state.Status == "deleting") {
			return channel.TelegramTarget{}, domain.ErrConversationDeleted
		}
	}
	return t, nil
}

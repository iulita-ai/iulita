package slack

import (
	"context"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/domain"
)

// parseChannelID recovers the real Slack channel ID from a composite chatID.
//
//	"slack:D123"      -> "D123"   (DM)
//	"slack:C123:U456" -> "C123"   (channel; user suffix dropped)
//
// Returns "" when chatID is not a "slack:" composite.
func parseChannelID(chatID string) string {
	rest, ok := strings.CutPrefix(chatID, "slack:")
	if !ok || rest == "" {
		return ""
	}
	if i := strings.IndexByte(rest, ':'); i >= 0 {
		return rest[:i]
	}
	return rest
}

// resolveMeta returns the Slack coordinates for a chatID, recovering them even
// when the in-memory chatMeta cache was evicted (1h TTL) or lost on restart.
// Resolution order:
//
//  1. in-memory chatMeta cache — fast path; carries threadTS + inboundTS;
//  2. persisted slack_chat_routes row — survives restart; re-hydrates the cache;
//  3. parse the channel straight out of the composite chatID — last resort;
//     posts land at the channel root because threadTS is unrecoverable.
//
// Returns nil only when chatID is not a valid "slack:" composite. Because
// step 3 always succeeds for a valid chatID, proactive delivery no longer
// depends on a live cache entry.
//
// resolution ≠ authorization: resolveMeta only locates a destination. It never
// implies the bot may write there — the future per-channel write gate (canWrite)
// must live in the send path and be fail-closed, treating neither a persisted
// row nor a parseable chatID as evidence of permission.
func (c *Channel) resolveMeta(ctx context.Context, chatID string) *chatMeta {
	if m := c.getChatMeta(chatID); m != nil {
		return m
	}

	if c.store != nil {
		if r, err := c.store.GetSlackRoute(ctx, c.instanceID, chatID); err != nil {
			c.logger.Warn("slack: failed to load persisted route",
				zap.String("chat_id", chatID), zap.Error(err))
		} else if r != nil {
			// Re-hydrate only if absent: a concurrent inbound may hold richer
			// meta (inboundTS/skipBookmark/fresher threadTS) we must not clobber.
			return c.storeChatMetaIfAbsent(chatID, &chatMeta{
				channelID: r.ChannelID,
				threadTS:  r.ThreadTS,
				userID:    r.SlackUserID,
				locale:    r.Locale,
			})
		}
	}

	if ch := parseChannelID(chatID); ch != "" {
		return &chatMeta{channelID: ch}
	}
	return nil
}

// persistRoute best-effort saves the Slack coordinates behind a chatID so that
// proactive delivery survives cache eviction and restarts. It runs the DB write
// on a tracked background goroutine so the single socket event-loop goroutine
// (which calls this on every inbound) never blocks on SQLite write contention.
// The route fields are copied before dispatch, so the goroutine shares no mutable
// state with the caller. Failures are logged, not propagated.
func (c *Channel) persistRoute(chatID string, meta *chatMeta) {
	if c.store == nil {
		return
	}
	route := &domain.SlackChatRoute{
		ChatID:      chatID,
		InstanceID:  c.instanceID,
		ChannelID:   meta.channelID,
		SlackUserID: meta.userID,
		ThreadTS:    meta.threadTS,
		Locale:      meta.locale,
	}
	// wg.Add runs on the event-loop goroutine (same one that calls wg.Wait at
	// shutdown), so it cannot race the Wait.
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := c.store.UpsertSlackRoute(ctx, route); err != nil {
			c.logger.Warn("slack: failed to persist route",
				zap.String("chat_id", route.ChatID), zap.Error(err))
		}
	}()
}

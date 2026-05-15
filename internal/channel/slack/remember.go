package slack

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	slackapi "github.com/slack-go/slack"
	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/bookmark"
	"github.com/iulita-ai/iulita/internal/i18n"
)

// rememberEntry stores metadata for a pending bookmark button.
type rememberEntry struct {
	slackChannel string // Slack channel ID for API calls
	slackUserID  string // Slack user ID who owns this bookmark (security validation)
	messageTS    string // Slack message timestamp (acts as message ID)
	content      string // full response text
	chatID       string // iulita chat ID
	userID       string // iulita user UUID
	locale       string
	createdAt    time.Time
}

// rememberState tracks pending bookmark buttons.
type rememberState struct {
	mu      sync.Mutex
	entries map[string]*rememberEntry // actionID → entry
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

// handleBookmarkCallback processes a Block Kit "remember:..." action.
// Returns true if the action was handled.
func (c *Channel) handleBookmarkCallback(actionID, senderUserID string) bool {
	if !strings.HasPrefix(actionID, "remember:") {
		return false
	}

	entry, ok := c.remembers.take(actionID)
	if !ok {
		return true // already handled or expired
	}

	tag := i18n.ResolveLocale(entry.locale, "en")

	// Verify the callback sender matches the message recipient.
	if entry.slackUserID != "" && senderUserID != entry.slackUserID {
		c.logger.Warn("bookmark ownership mismatch",
			zap.String("requested_by", senderUserID),
			zap.String("owned_by", entry.slackUserID))
		c.client.PostEphemeral(entry.slackChannel, senderUserID, //nolint:errcheck,gosec
			slackapi.MsgOptionText(i18n.Tl(tag, "BookmarkNotYours"), false))
		return true
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := c.rememberSvc.Save(ctx, entry.chatID, entry.userID, entry.content)
	if err != nil {
		c.logger.Error("bookmark save failed",
			zap.Error(err),
			zap.String("chat_id", entry.chatID))
		errMsg := i18n.Tl(tag, "BookmarkError")
		c.client.PostEphemeral(entry.slackChannel, senderUserID, //nolint:errcheck,gosec
			slackapi.MsgOptionText(errMsg, false))
		return true
	}

	// Update message: remove button, add "Saved" text.
	savedLabel := i18n.Tl(tag, "BookmarkSaved")
	savedBtn := slackapi.NewButtonBlockElement("noop", "noop",
		slackapi.NewTextBlockObject("plain_text", "✅ "+savedLabel, false, false))
	savedBtn.Style = ""

	_, _, _, err = c.client.UpdateMessage( //nolint:dogsled
		entry.slackChannel,
		entry.messageTS,
		slackapi.MsgOptionBlocks(
			slackapi.NewSectionBlock(
				slackapi.NewTextBlockObject("mrkdwn", ToMrkdwn(entry.content), false, false),
				nil, nil,
			),
			slackapi.NewContextBlock("",
				slackapi.NewTextBlockObject("mrkdwn", "✅ "+savedLabel, false, false),
			),
		),
	)
	if err != nil {
		c.logger.Debug("failed to update bookmark message", zap.Error(err))
	}

	return true
}

// generateNonce creates a short random hex string for callback data.
func generateNonce() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

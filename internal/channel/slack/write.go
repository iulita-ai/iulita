package slack

import (
	"context"
	"errors"
	"time"

	slackapi "github.com/slack-go/slack"
	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/ratelimit"
	"github.com/iulita-ai/iulita/internal/security"
)

// Write-path sentinel errors. Callers (the slack_post skill) map these to
// user-facing messages.
var (
	// ErrWriteDenied means the channel is not writable (unknown, not in the
	// allow-list, or write mode is off). Fail-closed default.
	ErrWriteDenied = errors.New("slack: posting to this channel is not permitted")
	// ErrGuardrailBlocked means an auto-mode guardrail (hourly budget / quiet
	// hours) blocked the post.
	ErrGuardrailBlocked = errors.New("slack: post blocked by a guardrail")
	// ErrSecretDetected means the outgoing text looked like it contained a secret.
	ErrSecretDetected = errors.New("slack: refusing to post text that appears to contain a secret")
)

// writeAPI is the narrow slack-go surface the write path uses (injectable for
// tests). *slackapi.Client satisfies it.
type writeAPI interface {
	PostMessageContext(ctx context.Context, channelID string, options ...slackapi.MsgOption) (string, string, error)
}

// writeConfig is the parsed write-permission slice of SlackInstanceConfig.
type writeConfig struct {
	channels   map[string]struct{} // exact-match writable channel IDs
	mode       string              // "off" | "draft" | "auto"; anything else => off
	quietStart int                 // hour [0,23]
	quietEnd   int                 // hour [0,23]; (0,0) => quiet hours not configured
}

// SetWriteConfig installs the channel write policy. maxPerHour<=0 disables the
// per-channel post budget. mode is normalized to "off" unless it is exactly
// "draft" or "auto" (fail-closed: an unset/legacy mode is never writable).
func (c *Channel) SetWriteConfig(channels []string, mode string, maxPerHour int, quietHours [2]int) {
	set := make(map[string]struct{}, len(channels))
	for _, ch := range channels {
		if ch != "" {
			set[ch] = struct{}{}
		}
	}
	if mode != "draft" && mode != "auto" {
		mode = "off"
	}
	// Fail-closed: "auto" without a positive hourly cap would post autonomously with
	// no rate limit. The UI forbids this, but a hand-crafted config could set it, so
	// downgrade to approval-gated "draft" here rather than trust the caller.
	if mode == "auto" && maxPerHour <= 0 {
		c.logger.Warn("slack: auto write mode requires a positive max_posts_per_hour; downgrading to draft")
		mode = "draft"
	}
	c.writeMu.Lock()
	c.writeCfg = writeConfig{
		channels:   set,
		mode:       mode,
		quietStart: quietHours[0],
		quietEnd:   quietHours[1],
	}
	if maxPerHour > 0 {
		c.postLimiter = ratelimit.New(maxPerHour, time.Hour)
	} else {
		c.postLimiter = nil
	}
	c.writeMu.Unlock()
}

// canWrite is the single write-permission gate. It never touches the network and
// is fail-closed: an unknown channel, a channel not in the allow-list, or a mode
// other than draft/auto returns ok=false.
func (c *Channel) canWrite(channelID string) (mode string, ok bool) {
	c.writeMu.RLock()
	defer c.writeMu.RUnlock()
	if channelID == "" || (c.writeCfg.mode != "draft" && c.writeCfg.mode != "auto") {
		return "off", false
	}
	if _, allowed := c.writeCfg.channels[channelID]; !allowed {
		return "off", false
	}
	return c.writeCfg.mode, true
}

// WriteEnabled reports whether this instance can post to at least one channel
// (mode is draft/auto AND the allow-list is non-empty). Used to gate the
// slack_write capability.
func (c *Channel) WriteEnabled() bool {
	c.writeMu.RLock()
	defer c.writeMu.RUnlock()
	return (c.writeCfg.mode == "draft" || c.writeCfg.mode == "auto") && len(c.writeCfg.channels) > 0
}

// WriteMode reports the effective write mode for a channel ("off" when not
// writable), for callers that need the mode without attempting a post.
func (c *Channel) WriteMode(channelID string) string {
	mode, ok := c.canWrite(channelID)
	if !ok {
		return "off"
	}
	return mode
}

// PostToChannel is the ONLY path that posts to an allow-listed channel by ID. It
// re-checks canWrite, runs a last-resort secret scan (non-bypassable, since this
// is the chokepoint), and enforces the per-channel hourly budget + quiet hours.
func (c *Channel) PostToChannel(ctx context.Context, channelID, text string) (string, error) {
	mode, ok := c.canWrite(channelID)
	if !ok {
		return "", ErrWriteDenied
	}
	if matched, pattern := security.Contains(text); matched {
		c.logger.Warn("slack: refusing to post text with a suspected secret",
			zap.String("channel", channelID), zap.String("pattern", pattern))
		return "", ErrSecretDetected
	}
	// Quiet hours and the hourly budget apply to AUTONOMOUS posts only. A draft the
	// owner just approved is an explicit, per-post decision and must not be silently
	// swallowed by a guardrail. (The budget slot is consumed on attempt, not on
	// success — a transient send failure over-counts slightly, which is fail-safe.)
	if mode == "auto" {
		if c.inQuietHours(time.Now()) {
			return "", ErrGuardrailBlocked
		}
		c.writeMu.RLock()
		limiter := c.postLimiter
		c.writeMu.RUnlock()
		if limiter != nil && !limiter.Allow(channelID) {
			return "", ErrGuardrailBlocked
		}
	}

	api := c.postAPI
	if api == nil {
		api = c.client
	}
	_, ts, err := api.PostMessageContext(ctx, channelID,
		slackapi.MsgOptionText(ToMrkdwn(text), false),
		slackapi.MsgOptionDisableLinkUnfurl(),
	)
	if err != nil {
		return "", err
	}
	return ts, nil
}

// inQuietHours reports whether t's hour falls in the configured quiet window.
// (0,0) means "not configured" and never blocks. Windows may wrap midnight
// (e.g. start=22, end=8 → quiet 22:00–07:59).
func (c *Channel) inQuietHours(t time.Time) bool {
	c.writeMu.RLock()
	start, end := c.writeCfg.quietStart, c.writeCfg.quietEnd
	c.writeMu.RUnlock()
	if start == 0 && end == 0 {
		return false
	}
	h := t.Hour()
	if start <= end {
		return h >= start && h < end
	}
	return h >= start || h < end // wraps midnight
}

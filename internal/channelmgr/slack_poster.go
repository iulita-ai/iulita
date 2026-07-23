package channelmgr

import (
	"context"

	slackch "github.com/iulita-ai/iulita/internal/channel/slack"
)

// This file implements the ChannelPoster seam the slack_post skill depends on,
// routing across running Slack instances, plus the slack_write capability toggle.

// pickWritable returns the running Slack instance that owns posts to channelID,
// or nil if none can write there. Selection is DETERMINISTIC (lowest instance ID
// among those that allow the channel) so that WriteMode and PostToChannel always
// resolve the SAME instance — otherwise two independent map walks could let the
// skill decide "auto" from one instance and post through another (draft) one,
// bypassing approval + quiet-hours. Caller must not hold m.mu.
func (m *Manager) pickWritable(channelID string) *slackch.Channel {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var bestID string
	var best *slackch.Channel
	for id, mc := range m.running {
		if mc.slack != nil && mc.slack.WriteMode(channelID) != "off" {
			if best == nil || id < bestID {
				best, bestID = mc.slack, id
			}
		}
	}
	return best
}

// WriteMode returns the effective write mode ("off"/"draft"/"auto") for a channel
// on the resolved instance.
func (m *Manager) WriteMode(_ context.Context, channelID string) string {
	if ch := m.pickWritable(channelID); ch != nil {
		return ch.WriteMode(channelID)
	}
	return "off"
}

// PostToChannel routes the post to the resolved instance (the same one WriteMode
// resolved). Fails closed (ErrWriteDenied) if none can write there.
func (m *Manager) PostToChannel(ctx context.Context, channelID, text string) (string, error) {
	// The resolved *Channel is used after RUnlock; that is safe — posting is an
	// HTTP call via the bot client, independent of the Socket Mode listener, and
	// PostToChannel re-checks canWrite on the instance.
	ch := m.pickWritable(channelID)
	if ch == nil {
		return "", slackch.ErrWriteDenied
	}
	return ch.PostToChannel(ctx, channelID, text)
}

// SetSlackWriteCapability registers a callback used to enable/disable the
// slack_write capability, then fires it once for the current state.
func (m *Manager) SetSlackWriteCapability(fn func(enabled bool)) {
	m.mu.Lock()
	m.slackWriteCapFn = fn
	m.mu.Unlock()
	m.recomputeSlackWrite()
}

// recomputeSlackWrite recalculates whether any running Slack instance is
// write-enabled and notifies the capability callback. The slackWriteMu serializes
// concurrent recomputes so snapshot+notify is atomic (otherwise a start/stop race
// could deliver an older state last and leave the capability stale).
func (m *Manager) recomputeSlackWrite() {
	m.slackWriteMu.Lock()
	defer m.slackWriteMu.Unlock()

	m.mu.RLock()
	fn := m.slackWriteCapFn
	enabled := false
	for _, mc := range m.running {
		if mc.slack != nil && mc.slack.WriteEnabled() {
			enabled = true
			break
		}
	}
	m.mu.RUnlock()
	if fn != nil {
		fn(enabled)
	}
}

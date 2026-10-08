package channelmgr

import (
	"context"
	"strconv"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/channel/telegram"
)

// locationTargetFor resolves the Telegram channel + numeric chat ID for a send:
// DB lookup (user_channels.channel_instance_id) first; if the chat maps to a
// NON-Telegram instance → unsupported. When the chat has no DB instance record
// (the column is not populated today) and the chatID is numeric, the single
// running Telegram instance is used — but with TWO OR MORE running Telegram
// instances the target is ambiguous (sending via the wrong bot would deliver
// the user's pin into a different conversation), so the send is unsupported and
// the skill degrades to the map link. Determinism is implemented here on
// purpose — the older numeric fallbacks (NotifyStatus, firstRunning) iterate a
// Go map and are nondeterministic; pickWritable (slack_poster.go) is the
// precedent. Returns channel.ErrLocationUnsupported when no unambiguous
// Telegram instance can serve the chat.
func (m *Manager) locationTargetFor(ctx context.Context, chatID string) (*telegram.Channel, int64, error) {
	chatID64, err := strconv.ParseInt(chatID, 10, 64)
	if err != nil {
		return nil, 0, channel.ErrLocationUnsupported // "console", web:*, slack:*
	}
	// A DB binding decides the bot — even when its instance is NOT running
	// (stopped/crashed) or is not a Telegram instance: fail closed instead of
	// falling through to the numeric fallback, which could deliver the pin via
	// a different bot into a wrong conversation. A DB LOOKUP FAILURE also
	// fails closed (lookupInstanceForChat collapses it into "" = "unbound",
	// which would re-enable the fallback path) — hence the direct store call.
	instanceID, err := m.store.GetChannelInstanceIDByChat(ctx, chatID)
	if err != nil {
		m.logger.Debug("channel instance lookup failed, refusing location send",
			zap.String("chat_id", chatID), zap.Error(err))
		return nil, 0, channel.ErrLocationUnsupported
	}
	if instanceID != "" {
		m.mu.RLock()
		mc := m.running[instanceID]
		m.mu.RUnlock()
		if mc == nil || mc.tg == nil {
			return nil, 0, channel.ErrLocationUnsupported
		}
		return mc.tg, chatID64, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	tg, count := pickTelegram(m.running)
	if tg == nil || count > 1 {
		return nil, 0, channel.ErrLocationUnsupported
	}
	return tg, chatID64, nil
}

// pickTelegram deterministically selects the lowest-instance-ID running
// Telegram channel and reports how many are running. Pure → directly testable.
func pickTelegram(running map[string]*ManagedChannel) (best *telegram.Channel, count int) {
	var bestID string
	for id, mc := range running {
		if mc.tg != nil {
			count++
			if best == nil || id < bestID {
				best, bestID = mc.tg, id
			}
		}
	}
	return best, count
}

// SendLocationToChat sends a native map pin to a chat. Implements
// channel.LocationSender.
func (m *Manager) SendLocationToChat(ctx context.Context, chatID string, loc channel.OutboundLocation) (int, error) {
	tg, chatID64, err := m.locationTargetFor(ctx, chatID)
	if err != nil {
		return 0, err
	}
	return tg.SendLocation(chatID64, loc)
}

// SendVenueToChat sends a native venue card to a chat. Implements
// channel.LocationSender.
func (m *Manager) SendVenueToChat(ctx context.Context, chatID string, v channel.OutboundVenue) (int, error) {
	tg, chatID64, err := m.locationTargetFor(ctx, chatID)
	if err != nil {
		return 0, err
	}
	return tg.SendVenue(chatID64, v)
}

// CanSendLocations reports whether a native pin can be sent to chatID.
// Implements channel.LocationSender.
func (m *Manager) CanSendLocations(chatID string) bool {
	_, _, err := m.locationTargetFor(context.Background(), chatID)
	return err == nil
}

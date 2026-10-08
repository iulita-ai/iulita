package telegram

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/security"
)

// MaxLocationSendsPerHour is the per-chat hourly budget for native location
// sends; exported so the share_location refusal string cannot drift from the
// limiter wiring.
const MaxLocationSendsPerHour = 10

var (
	// ErrLocationCapped is returned when the per-chat hourly location budget is spent.
	ErrLocationCapped = errors.New("telegram: location send cap reached")
	// ErrLocationSecret is returned when venue text contains a suspected secret.
	ErrLocationSecret = errors.New("telegram: venue text contains a suspected secret")
)

// SendLocation sends a native map pin. The ONLY outbound location path on this
// channel (non-bypassable chokepoint, slack write.go model): re-validates
// coordinates and enforces the per-chat hourly budget. Budget slots are
// consumed on attempt, not success (fail-safe over-count). Implements the
// telegram half of channel.LocationSender via channelmgr routing.
func (c *Channel) SendLocation(chatID int64, loc channel.OutboundLocation) (int, error) {
	if err := channel.ValidateCoords(loc.Latitude, loc.Longitude); err != nil {
		return 0, err
	}
	if err := c.allowLocationSend(chatID); err != nil {
		return 0, err
	}
	lat, lon := nudgeZeroAxis(round6(loc.Latitude)), nudgeZeroAxis(round6(loc.Longitude))
	cfg := tgbotapi.NewLocation(chatID, lat, lon)
	cfg.HorizontalAccuracy = channel.ClampAccuracy(loc.Accuracy)
	msg, err := c.bot.Send(cfg)
	if err != nil {
		return 0, fmt.Errorf("sending location: %w", sanitizeTGError(err))
	}
	c.logger.Info("location sent",
		zap.Int64("chat_id", chatID),
		zap.String("kind", "pin"),
		zap.Int("message_id", msg.MessageID))
	return msg.MessageID, nil
}

// SendVenue sends a native venue card through the same chokepoint.
func (c *Channel) SendVenue(chatID int64, v channel.OutboundVenue) (int, error) {
	if err := channel.ValidateCoords(v.Location.Latitude, v.Location.Longitude); err != nil {
		return 0, err
	}
	if matched, pattern := security.Contains(v.Title + "\n" + v.Address); matched {
		c.logger.Warn("telegram: refusing venue with suspected secret",
			zap.Int64("chat_id", chatID),
			zap.String("kind", "venue"),
			zap.String("pattern", pattern))
		return 0, ErrLocationSecret
	}
	if err := c.allowLocationSend(chatID); err != nil {
		return 0, err
	}
	cfg := venueConfig(chatID, v)
	msg, err := c.bot.Send(cfg)
	if err != nil {
		return 0, fmt.Errorf("sending venue: %w", sanitizeTGError(err))
	}
	c.logger.Info("location sent",
		zap.Int64("chat_id", chatID),
		zap.String("kind", "venue"),
		zap.Int("message_id", msg.MessageID))
	return msg.MessageID, nil
}

// allowLocationSend enforces the per-chat hourly budget. locLimiter is set
// once in New (constant cap) — no swap, no lock needed.
func (c *Channel) allowLocationSend(chatID int64) error {
	if c.locLimiter != nil && !c.locLimiter.Allow(strconv.FormatInt(chatID, 10)) {
		return ErrLocationCapped
	}
	return nil
}

// sanitizeTGError strips *url.Error, whose URL embeds the bot token
// (api.telegram.org/bot<TOKEN>/…) — transport failures (DNS, timeout, TLS)
// must not leak the token into Error-level logs. Telegram API errors
// (*tgbotapi.Error) pass through verbatim.
func sanitizeTGError(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return uerr.Err
	}
	return err
}

// venueConfig builds the sendVenue request, bounding title/address to the Bot
// API limits (128/256 chars) the same way the inbound path bounds untrusted
// venue text — a long geocoder display-name segment must not 400 the send.
func venueConfig(chatID int64, v channel.OutboundVenue) tgbotapi.VenueConfig {
	lat, lon := nudgeZeroAxis(round6(v.Location.Latitude)), nudgeZeroAxis(round6(v.Location.Longitude))
	return tgbotapi.NewVenue(chatID, truncateRunes(v.Title, 128), truncateRunes(v.Address, 256), lat, lon)
}

// round6 rounds coordinates to 6 decimals (≈0.1 m) so every outbound surface
// carries the same canonical precision the string renderers use (plan §12).
func round6(v float64) float64 { return math.Round(v*1e6) / 1e6 }

// nudgeZeroAxis works around tgbotapi v5.5.1 serialization: LocationConfig and
// VenueConfig both encode lat/lon with AddNonZeroFloat, which OMITS exactly-0.0
// values, making the API call fail. A 1e-8 degree (≈1 mm) nudge is harmless for
// any real pin but removes that API-error class.
func nudgeZeroAxis(v float64) float64 {
	if v == 0 {
		return 1e-8
	}
	return v
}

package telegram

import (
	"unicode/utf8"

	"golang.org/x/text/language"

	"github.com/go-telegram/bot/models"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/i18n"
)

// locationFromMessage extracts the shared location or venue, venue-first.
// Returns nil when the message carries neither. Telegram sets Message.Location
// whenever Message.Venue is set, so reading Venue first yields exactly ONE
// attachment with Title/Address filled.
func locationFromMessage(m *models.Message) *channel.LocationAttachment {
	if m == nil {
		return nil
	}
	if v := m.Venue; v != nil {
		return &channel.LocationAttachment{
			Latitude:  v.Location.Latitude,
			Longitude: v.Location.Longitude,
			Title:     truncateRunes(v.Title, 256), // untrusted text: bound payload size
			Address:   truncateRunes(v.Address, 256),
		}
	}
	if l := m.Location; l != nil {
		return &channel.LocationAttachment{
			Latitude:  l.Latitude,
			Longitude: l.Longitude,
			Accuracy:  l.HorizontalAccuracy, // 0-1500 m, 0 = absent
			Live:      l.LivePeriod > 0,     // heading/proximity ignored in v1
		}
	}
	return nil
}

// formatLocationText renders the localized line injected into msg.Text
// (TelegramVoicePrefix precedent). Coordinates are dot-decimal %.6f built in
// Go so no translator can reformat numbers.
func formatLocationText(localeTag language.Tag, loc *channel.LocationAttachment) string {
	coords := channel.FormatCoords(loc.Latitude, loc.Longitude) // "52.516270, 13.377750"
	if localeTag == language.Hebrew {
		// Keep digits and the comma readable inside RTL text (U+200E LRM marks).
		coords = "\u200e" + coords + "\u200e"
	}
	if loc.Title != "" {
		line := i18n.Tl(localeTag, "TelegramVenuePrefix", map[string]any{"Title": loc.Title})
		if loc.Address != "" {
			line += i18n.Tl(localeTag, "TelegramVenueAddressSuffix", map[string]any{"Address": loc.Address})
		}
		return line + " (" + coords + ")"
	}
	line := i18n.Tl(localeTag, "TelegramLocationPrefix", map[string]any{"Coords": coords})
	if loc.Accuracy > 0 {
		line += i18n.Tl(localeTag, "TelegramLocationAccuracySuffix", map[string]any{"Meters": int(loc.Accuracy)})
	}
	if loc.Live {
		line += i18n.Tl(localeTag, "TelegramLocationLiveSuffix")
	}
	return line
}

// truncateRunes bounds untrusted venue text without splitting multibyte runes.
func truncateRunes(s string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(s) <= limit {
		return s
	}
	runes := []rune(s)
	return string(runes[:limit])
}

// hasUpdateContent reports whether the update carries any content type the
// channel forwards: text, photo, document, voice, audio, location or venue.
// Stickers, animations and other unsupported types return false.
func hasUpdateContent(m *models.Message) bool {
	if m == nil {
		return false
	}
	return m.Text != "" || len(m.Photo) > 0 || m.Document != nil ||
		m.Voice != nil || m.Audio != nil || m.Location != nil || m.Venue != nil
}

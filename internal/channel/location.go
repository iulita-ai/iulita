package channel

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
)

// ErrLocationUnsupported is returned when no location-capable channel can
// serve a chat (non-Telegram chat or no running Telegram instance).
var ErrLocationUnsupported = errors.New("location sending not supported for this chat")

// OutboundLocation is a native map pin to send on a location-capable channel.
type OutboundLocation struct {
	Latitude  float64
	Longitude float64
	Accuracy  float64 // horizontal accuracy in meters, 0 = omit
}

// OutboundVenue is a native venue card (title + address + pin).
type OutboundVenue struct {
	Location OutboundLocation
	Title    string
	Address  string
}

// ValidateCoords enforces finite, in-range coordinates at a typed boundary so
// malformed LLM payloads can never reach a Telegram API call. Errors carry no
// coordinate values (they may be logged).
func ValidateCoords(lat, lon float64) error {
	switch {
	case math.IsNaN(lat) || math.IsNaN(lon) || math.IsInf(lat, 0) || math.IsInf(lon, 0):
		return fmt.Errorf("coordinates must be finite")
	case lat < -90 || lat > 90:
		return fmt.Errorf("latitude out of range")
	case lon < -180 || lon > 180:
		return fmt.Errorf("longitude out of range")
	}
	return nil
}

// FormatCoords renders canonical dot-decimal coordinates ("52.516270, 13.377750").
// The single source of the %.6f precision rule (plan §12) — every surface
// (injected marker lines, geocode output, OSM links) renders through it.
func FormatCoords(lat, lon float64) string {
	return strconv.FormatFloat(lat, 'f', 6, 64) + ", " + strconv.FormatFloat(lon, 'f', 6, 64)
}

// ClampAccuracy bounds horizontal accuracy to the Bot API range [0, 1500] m.
// NaN fails both range comparisons, so it is handled explicitly — a NaN would
// serialize as the literal "NaN" into the form body and fail the API call.
func ClampAccuracy(a float64) float64 {
	if a <= 0 || math.IsNaN(a) {
		return 0
	}
	if a > 1500 {
		return 1500
	}
	return a
}

// LocationSender sends native location messages. Implemented by
// channelmgr.Manager (instance routing); declared here so skills never import
// channelmgr (slackpost ChannelPoster precedent).
type LocationSender interface {
	// CanSendLocations reports whether a native pin can be sent to chatID.
	CanSendLocations(chatID string) bool
	// SendLocationToChat sends a map pin; returns the platform message ID.
	SendLocationToChat(ctx context.Context, chatID string, loc OutboundLocation) (int, error)
	// SendVenueToChat sends a venue card; returns the platform message ID.
	SendVenueToChat(ctx context.Context, chatID string, v OutboundVenue) (int, error)
}

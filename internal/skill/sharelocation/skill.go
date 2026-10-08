// Package sharelocation implements the share_location tool: send a place to
// the user as a native map pin / venue card on the current chat's channel,
// falling back to an OpenStreetMap link on channels without native support.
package sharelocation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/channel"
	telegramch "github.com/iulita-ai/iulita/internal/channel/telegram"
	"github.com/iulita-ai/iulita/internal/eventbus"
	"github.com/iulita-ai/iulita/internal/security"
	"github.com/iulita-ai/iulita/internal/skill"
)

const (
	// outcome values for the location.sent event payload.
	outcomeSent     = "sent"
	outcomeFallback = "fallback"
	outcomeInvalid  = "invalid"
	outcomeError    = "error"
)

// Skill is the share_location tool.
type Skill struct {
	sender channel.LocationSender // nil until SetLocationSender (deferred wiring)
	bus    *eventbus.Bus          // nil-safe; observability
	logger *zap.Logger
}

// New constructs the skill. The location sender is wired later via
// SetLocationSender (after channelmgr exists).
func New(logger *zap.Logger) *Skill {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Skill{logger: logger}
}

// SetLocationSender wires the native-send seam (built after skill registration).
func (s *Skill) SetLocationSender(ls channel.LocationSender) { s.sender = ls }

// SetBus wires the observability event bus (deferred wiring).
func (s *Skill) SetBus(bus *eventbus.Bus) { s.bus = bus }

// Name is the tool name exposed to the LLM.
func (s *Skill) Name() string { return "share_location" }

// Description tells the model what the tool does.
func (s *Skill) Description() string {
	return "Send a place to the user as a native map pin or venue card in the current chat " +
		"(native on Telegram, a map link on other channels). Use when the user asks to share, " +
		"send, show, or pin a place on the map. This tool only SENDS a place you already know — " +
		"it never detects the user's position and cannot look up place names itself."
}

// InputSchema is the JSON schema for the tool arguments. There is deliberately
// NO chat_id parameter: the target is always the chat the message came from.
func (s *Skill) InputSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "latitude":  { "type": "number", "minimum": -90,  "maximum": 90,
                   "description": "Latitude in decimal degrees." },
    "longitude": { "type": "number", "minimum": -180, "maximum": 180,
                   "description": "Longitude in decimal degrees." },
    "title":     { "type": "string",
                   "description": "Place name. When title AND address are both set, a venue card is sent instead of a bare pin." },
    "address":   { "type": "string",
                   "description": "Street address shown under the venue title." },
    "accuracy":  { "type": "number", "minimum": 0, "maximum": 1500,
                   "description": "Optional horizontal accuracy in meters." }
  },
  "required": ["latitude", "longitude"]
}`)
}

type shareInput struct {
	// Pointers distinguish "model omitted the argument / sent null" from a
	// legitimate 0 value — unmarshal leaves plain float64s at 0 silently, and
	// a one-sided omission would otherwise send a pin at (48.85, 0) off Greenwich.
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
	Title     string   `json:"title"`
	Address   string   `json:"address"`
	Accuracy  float64  `json:"accuracy"`
}

// Execute resolves one share attempt. It returns model-facing strings and nil
// errors so the LLM tool loop never retries (slackpost postError pattern).
func (s *Skill) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in shareInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	in.Title = strings.TrimSpace(in.Title)
	in.Address = strings.TrimSpace(in.Address)

	if in.Latitude == nil || in.Longitude == nil {
		s.publish(ctx, kindOf(in), outcomeInvalid)
		return "Invalid coordinates (latitude and longitude are both required). Ask the user to double-check the place.", nil
	}
	lat, lon := *in.Latitude, *in.Longitude
	chatID := skill.ChatIDFrom(ctx)

	if err := channel.ValidateCoords(lat, lon); err != nil {
		s.publish(ctx, kindOf(in), outcomeInvalid)
		return fmt.Sprintf("Invalid coordinates (%s). Ask the user to double-check the place.", err), nil
	}
	// Exactly (0,0) is the null island artifact — meaningless as a shared place.
	if lat == 0 && lon == 0 {
		s.publish(ctx, kindOf(in), outcomeInvalid)
		return "Invalid coordinates (0, 0 is not a real place). Ask the user to double-check the place.", nil
	}

	// Fail fast on secrets in venue text; the channel chokepoint re-checks.
	if matched, pattern := security.Contains(in.Title + "\n" + in.Address); matched {
		s.publish(ctx, kindOf(in), outcomeInvalid)
		s.logger.Warn("share_location refused: suspected secret in venue text",
			zap.String("chat_id", chatID),
			zap.String("kind", kindOf(in)),
			zap.String("pattern", pattern))
		return "I won't send that — the place name or address looks like it contains a credential or secret.", nil
	}

	link := mapLink(lat, lon)

	// Same-chat only: no chat_id argument exists; empty chatID (agent-job
	// contexts) and missing seam fail soft to the map link.
	if s.sender == nil || chatID == "" || !s.sender.CanSendLocations(chatID) {
		s.publish(ctx, kindOf(in), outcomeFallback)
		return "This chat does not support native map pins. Include this map link in your reply verbatim, as a plain URL on its own line (do NOT wrap it in a markdown link — some terminals drop link URLs): " + link, nil
	}

	loc := channel.OutboundLocation{
		Latitude:  lat,
		Longitude: lon,
		Accuracy:  channel.ClampAccuracy(in.Accuracy),
	}

	var msgID int
	var err error
	if in.Title != "" && in.Address != "" {
		msgID, err = s.sender.SendVenueToChat(ctx, chatID, channel.OutboundVenue{
			Location: loc, Title: in.Title, Address: in.Address})
	} else {
		msgID, err = s.sender.SendLocationToChat(ctx, chatID, loc)
	}

	if err != nil {
		s.publish(ctx, kindOf(in), outcomeError)
		if errors.Is(err, telegramch.ErrLocationCapped) {
			// Plan §13: cap refusals are distinguished by a Warn log with
			// chat_id + kind correlation fields (never coordinates).
			s.logger.Warn("location send cap reached for chat, serving map link instead",
				zap.String("chat_id", chatID),
				zap.String("kind", kindOf(in)))
			return fmt.Sprintf("Location send limit reached for this chat (%d per hour). Tell the user, and include this map link instead — as a plain URL on its own line, never inside a markdown link: ", telegramch.MaxLocationSendsPerHour) + link, nil
		}
		s.logger.Error("native location send failed, falling back to map link",
			zap.String("chat_id", chatID),
			zap.String("kind", kindOf(in)),
			zap.Error(err))
		return "Sending the native pin failed, so include this map link in your reply verbatim, as a plain URL on its own line (do NOT wrap it in a markdown link — some terminals drop link URLs): " + link, nil
	}

	s.publish(ctx, kindOf(in), outcomeSent)
	if in.Title != "" && in.Address != "" {
		return fmt.Sprintf("Native venue card sent above (message %d) showing %q and its address. Do not repeat the full address verbatim in your reply.", msgID, in.Title), nil
	}
	return fmt.Sprintf("Native map pin sent above (message %d). Do not repeat the coordinates or address in your reply unless asked.", msgID), nil
}

// kindOf reports the payload kind: a venue needs both title and address
// (anything else downgrades to a bare pin).
func kindOf(in shareInput) string {
	if in.Title != "" && in.Address != "" {
		return "venue"
	}
	return "pin"
}

// mapLink is the SHORT OSM form (no #map fragment): ≤~64 chars at %.6f, which
// survives the console TUI's glamour word wrap (WithWordWrap(70)) and terminal
// URL auto-detection; it works on every channel.
func mapLink(lat, lon float64) string {
	// FormatCoords is the single source of the %.6f precision rule (plan §12);
	// the split reuses it for both URL parameters.
	parts := strings.Split(channel.FormatCoords(lat, lon), ", ")
	return "https://www.openstreetmap.org/?mlat=" + parts[0] + "&mlon=" + parts[1]
}

// publish emits the coordinate-free observability event (nil-bus safe).
func (s *Skill) publish(ctx context.Context, kind, outcome string) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(ctx, eventbus.Event{
		Type:    eventbus.LocationSent,
		Payload: eventbus.LocationSentPayload{Kind: kind, Outcome: outcome},
	})
}

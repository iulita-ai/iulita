package assistant

import (
	"regexp"
	"strings"

	"github.com/iulita-ai/iulita/internal/domain"
)

// coordShapeRe matches coordinate-shaped PAIRS ("52.516270, 13.377750" from
// geocode output) and OSM map links (mlat=…&mlon=…) — the two coordinate
// carriers in skill output. Deliberately NOT a lone 4+-decimal number, so
// exchange rates and other single-precision floats in unrelated skill
// previews survive untouched.
var coordShapeRe = regexp.MustCompile(
	`-?\d{1,3}\.\d{4,}\s*,\s*-?\d{1,3}\.\d{4,}|(?:mlat|latitude)=-?\d+(?:\.\d+)?&(?:mlon|longitude)=-?\d+(?:\.\d+)?`)

// scrubPreview redacts coordinate-shaped substrings from a skill-result
// preview before it is logged at INFO.
func scrubPreview(s string) string {
	return coordShapeRe.ReplaceAllString(s, "[coords]")
}

// recentSharedLocation reports whether any of the last few user messages in
// history carries an injected shared-location marker. Keeps the geolocation
// force-trigger suppressed on FOLLOW-UP turns ("where am I?" right after
// sharing a pin) so the answer comes from the pin in history, not the server
// IP — the model can still call geolocation voluntarily for genuine IP
// questions.
func recentSharedLocation(history []domain.ChatMessage) bool {
	const window = 5 // recent user messages to scan
	seen := 0
	for i := len(history) - 1; i >= 0 && seen < window; i-- {
		if history[i].Role != domain.RoleUser {
			continue
		}
		seen++
		if strings.Contains(history[i].Content, "[Location]: ") ||
			strings.Contains(history[i].Content, "[Place]: ") {
			return true
		}
	}
	return false
}

// sharedLocationsDirective builds the "## Shared Locations" system prompt
// block (English by house convention; only the reply language is locale-aware
// via the ## Language directive). The caller gates injection: on turns that
// carry location attachments OR have a shared pin in recent history (the
// follow-up "yes, remember it" turn needs the §12 privacy rules too).
func sharedLocationsDirective() string {
	var b strings.Builder
	b.WriteString("The user's message (or a recent message in this conversation) contains shared location markers:\n")
	b.WriteString("- \"[Location]: lat, lon\" — a map pin the user shared.\n")
	b.WriteString("- \"[Place]: Title — Address (lat, lon)\" — a named venue the user shared.\n")
	b.WriteString("Rules:\n")
	b.WriteString("- Resolve \"here\", \"this place\", \"nearby\", \"at my location\" to the most recently\n")
	b.WriteString("  shared coordinates.\n")
	b.WriteString("- For weather at a shared place, pass the \"lat, lon\" string as the weather\n")
	b.WriteString("  skill's location parameter.\n")
	b.WriteString("- When acknowledging a shared venue or reporting weather for it, prefer the\n")
	b.WriteString("  venue's title over a generic descriptor; never substitute a different place.\n")
	b.WriteString("- If the latest user message contains ONLY a location with no question or\n")
	b.WriteString("  instruction: briefly acknowledge it and ask ONE short question about the\n")
	b.WriteString("  most likely intents (weather here, remembering the place, finding\n")
	b.WriteString("  something nearby).\n")
	b.WriteString("  Do not call any tools yet.\n")
	b.WriteString("- Never invent a place name for coordinates you cannot resolve; refer to the\n")
	b.WriteString("  pin descriptively (\"the pin you shared\") instead.\n")
	b.WriteString("- If several locations were shared, distinguish them as first/second or by\n")
	b.WriteString("  venue title; ask which one when ambiguous.\n")
	b.WriteString("- Do not print raw coordinates or map URLs in your reply unless the user asks.\n")
	b.WriteString("  Exception: when the share_location tool explicitly returns a map link to\n")
	b.WriteString("  include (channel without native support, send failure, or hourly limit),\n")
	b.WriteString("  relay that link exactly as the tool instructs.\n")
	b.WriteString("- When helping the user save/remember a place, prefer the place name and\n")
	b.WriteString("  address; include coordinates only when they are essential (e.g. home,\n")
	b.WriteString("  office) and round them to 3 decimals (~110 m); use full precision only\n")
	b.WriteString("  when the user explicitly asks for it.\n")
	b.WriteString("- If asked for arrival/departure-triggered reminders (\"remind me when I\n")
	b.WriteString("  leave/arrive\"), say the feature is not available yet and offer a time-based\n")
	b.WriteString("  alternative.\n")
	b.WriteString("- When the user asks you to share, send, or show a place on the map, call the\n")
	b.WriteString("  share_location tool. The native pin is sent while the tool runs, so it\n")
	b.WriteString("  appears ABOVE your reply: never write \"card incoming below\" — if you refer\n")
	b.WriteString("  to it, say the pin was sent above. Name the place and give context in your\n")
	b.WriteString("  text, but do NOT repeat the full address verbatim (the pin card shows it).\n")
	b.WriteString("  Never send back a location the user just shared unless they explicitly ask.")
	return b.String()
}

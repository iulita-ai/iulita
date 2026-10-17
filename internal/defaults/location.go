// Package defaults contains application fallbacks, never inferred user data.
package defaults

import "strings"

// Location fallbacks apply only when no explicit city or timezone is known.
const (
	City     = "Иркутск"
	Timezone = "Asia/Irkutsk"

	// LocationPrompt is shared by the legacy and native chat engines. A fallback
	// is not a claim about the user's physical location or profile settings.
	LocationPrompt = "When no city is known, use the application fallback Иркутск (Irkutsk, Russia); when no timezone is known, use Asia/Irkutsk (UTC+08:00). Explicit user instructions, shared coordinates and known preferences take precedence. For weather without a known city, omit location so the tool applies this fallback. Do not invent a city from language, server IP or earlier assistant guesses. Describe a fallback as a default, never as the user's detected or saved location."
)

// CityOrDefault preserves a supplied location and otherwise uses Irkutsk.
func CityOrDefault(city string) string {
	if city = strings.TrimSpace(city); city != "" {
		return city
	}
	return City
}

// TimezoneOrDefault preserves explicit zones, including UTC.
func TimezoneOrDefault(zone string) string {
	if zone = strings.TrimSpace(zone); zone != "" {
		return zone
	}
	return Timezone
}

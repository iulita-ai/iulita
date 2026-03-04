package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/skill/interact"
	"github.com/iulita-ai/iulita/internal/storage"
)

// resolveLocation determines the weather location by searching memory/insights,
// then prompting the user with interactive options.
func resolveLocation(ctx context.Context, store storage.Repository, registry SkillLookup, asker interact.PromptAsker) (string, error) {
	userID := skill.UserIDFrom(ctx)

	// Search facts and insights for location hints.
	var memoryLocation string
	if userID != "" {
		memoryLocation = findLocationInStorage(ctx, store, userID)
	}

	// Build options for the user.
	var options []interact.Option
	if memoryLocation != "" {
		options = append(options, interact.Option{
			ID:    "memory",
			Label: memoryLocation,
		})
	}
	options = append(options, interact.Option{
		ID:    "geolocate",
		Label: "Detect automatically (by IP)",
	})

	answer, err := asker.Ask(ctx, "Where should I show the weather?", options)
	if err != nil {
		// If prompting failed (no prompter, timeout), try geolocating.
		return geolocateCity(ctx, registry)
	}

	switch answer {
	case "memory":
		return cleanLocationForGeocoding(memoryLocation), nil
	case "geolocate":
		return geolocateCity(ctx, registry)
	default:
		// Free text input from user.
		return answer, nil
	}
}

// findLocationInStorage searches facts and insights for location-related content.
func findLocationInStorage(ctx context.Context, store storage.Repository, userID string) string {
	// Strategy 1: FTS search with location-related keywords.
	queries := []string{
		"location", "city", "live", "based", "home",
		"город", "живёт", "живет", "находится", "расположен", "местоположение",
	}
	for _, q := range queries {
		facts, _ := store.SearchFactsByUser(ctx, userID, q, 5)
		for _, f := range facts {
			if loc := tryExtractLocation(f.Content); loc != "" {
				return loc
			}
		}
		insights, _ := store.SearchInsightsByUser(ctx, userID, q, 5)
		for _, ins := range insights {
			if loc := tryExtractLocation(ins.Content); loc != "" {
				return loc
			}
		}
	}

	// Strategy 2: Scan recent facts for location patterns (FTS may miss them).
	recentFacts, _ := store.GetRecentFactsByUser(ctx, userID, 50)
	for _, f := range recentFacts {
		if loc := tryExtractLocation(f.Content); loc != "" {
			return loc
		}
	}

	// Strategy 3: Search insights for timezone/geolocation references.
	geoQueries := []string{"timezone", "IP", "geolocation", "Amsterdam", "часовой пояс"}
	for _, q := range geoQueries {
		insights, _ := store.SearchInsightsByUser(ctx, userID, q, 3)
		for _, ins := range insights {
			if loc := tryExtractLocation(ins.Content); loc != "" {
				return loc
			}
		}
	}

	return ""
}

// tryExtractLocation attempts to extract a location from fact/insight content.
// Returns empty string if the content doesn't appear to contain location info.
func tryExtractLocation(content string) string {
	lower := strings.ToLower(content)
	if containsLocationKeyword(lower) {
		return extractLocationFromFact(content)
	}
	return ""
}

// locationKeywords contains phrases (in multiple languages) that indicate location info.
var locationKeywords = []string{
	// English
	"live in", "lives in", "based in", "located in", "from",
	"city", "location", "reside", "moved to", "stay in",
	"timezone", "time zone",
	// Russian
	"живёт в", "живет в", "живу в", "находится в", "расположен в",
	"переехал в", "город", "местоположение", "из города",
	"часовой пояс", "часовом поясе",
}

func containsLocationKeyword(text string) bool {
	for _, kw := range locationKeywords {
		if strings.Contains(text, kw) {
			return true
		}
	}
	return false
}

// extractLocationFromFact extracts the location substring from a fact.
// e.g. "User lives in Berlin, Germany" → "Berlin, Germany"
// e.g. "Пользователь живёт в Амстердаме" → "Амстердаме"
// e.g. "timezone: Europe/Amsterdam" → "Amsterdam"
func extractLocationFromFact(content string) string {
	// Try timezone extraction first (e.g., "Europe/Amsterdam" → "Amsterdam").
	if city := extractCityFromTimezone(content); city != "" {
		return city
	}

	// Use case-insensitive search on the original string to avoid
	// byte-offset mismatches from ToLower on multi-byte UTF-8 characters.
	markers := []string{
		// English
		"live in ", "lives in ", "based in ", "located in ", "moved to ", "from ",
		// Russian
		"живёт в ", "живет в ", "живу в ", "находится в ", "расположен в ", "переехал в ", "из города ",
	}
	for _, m := range markers {
		idx := indexFold(content, m)
		if idx >= 0 {
			loc := strings.TrimSpace(content[idx+len(m):])
			loc = strings.TrimRight(loc, ".,;!?")
			if loc != "" {
				return loc
			}
		}
	}
	return strings.TrimSpace(content)
}

// extractCityFromTimezone extracts a city name from an IANA timezone string.
// e.g., "Europe/Amsterdam" → "Amsterdam", "Asia/Tokyo" → "Tokyo"
func extractCityFromTimezone(content string) string {
	// Look for IANA timezone pattern: Region/City
	for _, word := range strings.Fields(content) {
		// Strip surrounding punctuation.
		word = strings.Trim(word, ".,;:!?\"'()")
		parts := strings.Split(word, "/")
		if len(parts) == 2 || len(parts) == 3 {
			city := parts[len(parts)-1]
			city = strings.ReplaceAll(city, "_", " ")
			// Validate it looks like a timezone region.
			region := strings.ToLower(parts[0])
			switch region {
			case "europe", "asia", "america", "africa", "australia", "pacific", "atlantic", "indian":
				return city
			}
		}
	}
	return ""
}

// indexFold returns the byte index of the first case-insensitive occurrence of needle in s.
// Returns -1 if not found. Safe for multi-byte UTF-8.
func indexFold(s, needle string) int {
	ln := len(needle)
	if ln == 0 {
		return 0
	}
	if ln > len(s) {
		return -1
	}
	for i := 0; i <= len(s)-ln; i++ {
		if strings.EqualFold(s[i:i+ln], needle) {
			return i
		}
	}
	return -1
}

// geolocateCity looks up the geolocation skill in the registry and extracts city information.
func geolocateCity(ctx context.Context, registry SkillLookup) (string, error) {
	if registry == nil {
		return "", fmt.Errorf("no skill registry available")
	}
	geo, ok := registry.Get("geolocation")
	if !ok {
		return "", fmt.Errorf("geolocation skill not available")
	}
	result, err := geo.Execute(ctx, json.RawMessage(`{}`))
	if err != nil {
		return "", err
	}
	// Parse the geolocation output to extract city.
	city := extractField(result, "City:")
	region := extractField(result, "Region:")
	country := extractField(result, "Country:")

	if city != "" {
		loc := city
		if region != "" && region != city {
			loc += ", " + region
		}
		return loc, nil
	}
	if country != "" {
		return country, nil
	}
	return "", fmt.Errorf("geolocation returned no city or country")
}

// cleanLocationForGeocoding normalizes extracted location for better geocoding.
// Handles: parenthetical abbreviations, Russian prepositional case endings.
// e.g. "Санкт-Петербурге (СПБ)" → "СПБ" (abbreviation is more reliable for geocoding)
// e.g. "Москве" → "Москва" (strip prepositional case ending)
func cleanLocationForGeocoding(loc string) string {
	// If there's an abbreviation in parentheses, prefer it — abbreviations geocode reliably.
	if idx := strings.Index(loc, "("); idx > 0 {
		end := strings.Index(loc[idx:], ")")
		if end > 0 {
			abbr := strings.TrimSpace(loc[idx+1 : idx+end])
			if abbr != "" {
				return abbr
			}
		}
		// Strip parenthetical content even if empty.
		loc = strings.TrimSpace(loc[:idx])
	}

	// Strip common Russian prepositional case endings for city names.
	// "в Санкт-Петербурге" → "Санкт-Петербурге" → "Санкт-Петербург"
	// "в Москве" → "Москве" → "Москва"
	if strings.HasSuffix(loc, "е") {
		// Try dropping the trailing "е" — works for many city names.
		base := loc[:len(loc)-len("е")]
		// Common patterns: -бурге → -бург, -ске → -ск, -граде → -град
		if strings.HasSuffix(base, "г") || strings.HasSuffix(base, "к") ||
			strings.HasSuffix(base, "д") || strings.HasSuffix(base, "н") ||
			strings.HasSuffix(base, "м") || strings.HasSuffix(base, "л") {
			return base
		}
	}

	return loc
}

// extractField extracts a field value from "Key: Value\n" format.
func extractField(text, key string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, key) {
			val := strings.TrimPrefix(line, key)
			val = strings.TrimSpace(val)
			// Remove country code suffix like "Germany (DE)"
			if idx := strings.Index(val, " ("); idx > 0 {
				val = val[:idx]
			}
			return val
		}
	}
	return ""
}

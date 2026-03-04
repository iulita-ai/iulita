package weather

import (
	"context"
	"fmt"
	"strings"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/i18n"
)

// wmoDescription maps WMO weather codes to human-readable descriptions.
var wmoDescription = map[int]string{
	0:  "Clear sky",
	1:  "Mainly clear",
	2:  "Partly cloudy",
	3:  "Overcast",
	45: "Fog",
	48: "Depositing rime fog",
	51: "Light drizzle",
	53: "Moderate drizzle",
	55: "Dense drizzle",
	56: "Light freezing drizzle",
	57: "Dense freezing drizzle",
	61: "Slight rain",
	63: "Moderate rain",
	65: "Heavy rain",
	66: "Light freezing rain",
	67: "Heavy freezing rain",
	71: "Slight snow",
	73: "Moderate snow",
	75: "Heavy snow",
	77: "Snow grains",
	80: "Slight rain showers",
	81: "Moderate rain showers",
	82: "Violent rain showers",
	85: "Slight snow showers",
	86: "Heavy snow showers",
	95: "Thunderstorm",
	96: "Thunderstorm with slight hail",
	99: "Thunderstorm with heavy hail",
}

// WMODescription returns a human-readable description for a WMO code.
// Uses i18n when a context is provided, falls back to the English map.
func WMODescription(code int, ctx ...context.Context) string {
	if len(ctx) > 0 && ctx[0] != nil {
		key := fmt.Sprintf("WeatherWMO%d", code)
		result := i18n.T(ctx[0], key)
		if result != key {
			return result
		}
	}
	if desc, ok := wmoDescription[code]; ok {
		return desc
	}
	return fmt.Sprintf("Weather code %d", code)
}

// formatForecast renders a WeatherResult as text or markdown.
func formatForecast(result *WeatherResult, caps channel.ChannelCaps) string {
	var b strings.Builder

	if result.Location != "" {
		fmt.Fprintf(&b, "Weather for %s\n\n", result.Location)
	}

	if caps.Has(channel.CapMarkdown) {
		return formatMarkdown(&b, result)
	}
	return formatPlainText(&b, result)
}

func formatMarkdown(b *strings.Builder, result *WeatherResult) string {
	// Emphasize the resolved location to prevent LLM from substituting a different city.
	if result.Location != "" {
		fmt.Fprintf(b, "[Location: %s — present this exact city name to the user]\n\n", result.Location)
	}
	if len(result.Days) == 1 {
		d := result.Days[0]
		fmt.Fprintf(b, "**%s** — %s\n", d.Date.Format("Mon, Jan 2"), d.Description)
		fmt.Fprintf(b, "🌡 %+.0f..%+.0f°C", d.TempMinC, d.TempMaxC)
		if d.PrecipMM > 0 {
			fmt.Fprintf(b, " | 🌧 %.1f mm", d.PrecipMM)
		}
		if d.WindKph > 0 {
			fmt.Fprintf(b, " | 💨 %.0f km/h", d.WindKph)
		}
		if d.Humidity > 0 {
			fmt.Fprintf(b, " | 💧 %d%%", d.Humidity)
		}
		if d.UVIndex > 0 {
			fmt.Fprintf(b, " | ☀️ UV %.0f", d.UVIndex)
		}
		b.WriteString("\n")
		return b.String()
	}

	// Multi-day table.
	b.WriteString("| Date | Weather | Temp °C | Precip | Wind |\n")
	b.WriteString("|------|---------|---------|--------|------|\n")
	for _, d := range result.Days {
		fmt.Fprintf(b, "| %s | %s | %+.0f..%+.0f | %.1f mm | %.0f km/h |\n",
			d.Date.Format("Mon 02"),
			d.Description,
			d.TempMinC, d.TempMaxC,
			d.PrecipMM,
			d.WindKph,
		)
	}
	return b.String()
}

func formatPlainText(b *strings.Builder, result *WeatherResult) string {
	// Emphasize the resolved location to prevent LLM from substituting a different city.
	if result.Location != "" {
		fmt.Fprintf(b, "IMPORTANT: This weather data is for %s. Do NOT change or substitute the city name.\n\n", result.Location)
	}
	for i, d := range result.Days {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(b, "%s: %s\n", d.Date.Format("Mon, Jan 2"), d.Description)
		fmt.Fprintf(b, "  Temperature: %+.0f to %+.0f°C\n", d.TempMinC, d.TempMaxC)
		if d.PrecipMM > 0 {
			fmt.Fprintf(b, "  Precipitation: %.1f mm\n", d.PrecipMM)
		}
		if d.WindKph > 0 {
			fmt.Fprintf(b, "  Wind: %.0f km/h\n", d.WindKph)
		}
		if d.Humidity > 0 {
			fmt.Fprintf(b, "  Humidity: %d%%\n", d.Humidity)
		}
		if d.UVIndex > 0 {
			fmt.Fprintf(b, "  UV Index: %.0f\n", d.UVIndex)
		}
	}
	return b.String()
}

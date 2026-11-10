package weather

import (
	"context"
	"fmt"
	"strings"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/domain"
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

// formatForecast retains the model-facing location instructions and complete
// forecast. Optional source presence prevents missing values becoming real zero.
func formatForecast(result *WeatherResult, caps channel.ChannelCaps) string {
	var b strings.Builder
	if result.Location != "" && result.Location != sharedPinDescriptor {
		fmt.Fprintf(&b, "Weather for %s\n\n", result.Location)
	}
	if result.Current != nil {
		current := &domain.WeatherCard{Location: result.Location, Source: result.Source, Current: result.Current}
		b.WriteString(FormatCardText(context.Background(), current))
		b.WriteString("\n\nForecast:\n")
	} else {
		b.WriteString("Current conditions are unavailable; the following data is a forecast, not a current measurement.\n\n")
	}
	if caps.Has(channel.CapMarkdown) {
		return formatMarkdown(&b, result)
	}
	return formatPlainText(&b, result)
}
func dayMetric(d DayForecast, key string) *float64 {
	if v := d.Values; v != nil {
		switch key {
		case "min":
			return v.TempMinC
		case "max":
			return v.TempMaxC
		case "rain":
			return v.PrecipitationMM
		case "wind":
			return v.WindKph
		case "uv":
			return v.UVIndexMax
		}
		return nil
	}
	switch key {
	case "min":
		return value(d.TempMinC)
	case "max":
		return value(d.TempMaxC)
	case "rain":
		return value(d.PrecipMM)
	case "wind":
		return value(d.WindKph)
	case "uv":
		return value(d.UVIndex)
	}
	return nil
}
func formattedMetric(v *float64, format string) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf(format, *v)
}
func modelPlace(b *strings.Builder, result *WeatherResult, markdown bool) {
	if result.Location == sharedPinDescriptor {
		if markdown {
			b.WriteString("[Location: shared pin — refer to it descriptively in the user's language (the place/pin they shared); never substitute a different place]\n\n")
		} else {
			b.WriteString("IMPORTANT: This weather data is for the place/pin the user shared — refer to it descriptively in the user's language; never substitute a different place.\n\n")
		}
	} else if result.Location != "" {
		if markdown {
			fmt.Fprintf(b, "[Location: %s — present this exact city name to the user]\n\n", result.Location)
		} else {
			fmt.Fprintf(b, "IMPORTANT: This weather data is for %s. Do NOT change or substitute the city name.\n\n", result.Location)
		}
	}
}
func modelSample(b *strings.Builder, d DayForecast) {
	if s := d.Sample; s != nil {
		fmt.Fprintf(b, "%s:", s.Kind)
		if s.WindKph != nil {
			fmt.Fprintf(b, " wind %.0f km/h;", *s.WindKph)
		}
		if s.PrecipitationMM != nil {
			fmt.Fprintf(b, " precipitation %.1f mm;", *s.PrecipitationMM)
		}
		if s.HumidityPercent != nil {
			fmt.Fprintf(b, " humidity %d%%;", *s.HumidityPercent)
		}
		if s.UVIndex != nil {
			fmt.Fprintf(b, " UV %.0f;", *s.UVIndex)
		}
		b.WriteString(" not current observations or full-day aggregates.\n")
	}
}
func formatMarkdown(b *strings.Builder, result *WeatherResult) string {
	modelPlace(b, result, true)
	if len(result.Days) == 1 {
		d := result.Days[0]
		fmt.Fprintf(b, "**%s** — %s\n", d.Date.Format("Mon, Jan 2"), d.Description)
		fmt.Fprintf(b, "🌡 %s..%s°C", formattedMetric(dayMetric(d, "min"), "%+.0f"), formattedMetric(dayMetric(d, "max"), "%+.0f"))
		for _, m := range []struct{ key, format string }{{"rain", " | 🌧 %.1f mm"}, {"wind", " | 💨 %.0f km/h"}, {"uv", " | ☀️ UV %.0f"}} {
			if v := dayMetric(d, m.key); v != nil && (d.Values != nil || *v > 0) {
				fmt.Fprintf(b, m.format, *v)
			}
		}
		if d.Values == nil && d.Humidity > 0 {
			fmt.Fprintf(b, " | 💧 %d%%", d.Humidity)
		}
		b.WriteString("\n")
		modelSample(b, d)
		return b.String()
	}
	b.WriteString("| Date | Weather | Temp °C | Precip | Wind |\n|------|---------|---------|--------|------|\n")
	for _, d := range result.Days {
		fmt.Fprintf(b, "| %s | %s | %s..%s | %s | %s |\n", d.Date.Format("Mon 02"), d.Description, formattedMetric(dayMetric(d, "min"), "%+.0f"), formattedMetric(dayMetric(d, "max"), "%+.0f"), formattedMetric(dayMetric(d, "rain"), "%.1f mm"), formattedMetric(dayMetric(d, "wind"), "%.0f km/h"))
	}
	for _, d := range result.Days {
		if d.Sample != nil {
			fmt.Fprintf(b, "%s · ", d.Date.Format("2006-01-02"))
			modelSample(b, d)
		}
	}
	return b.String()
}
func formatPlainText(b *strings.Builder, result *WeatherResult) string {
	modelPlace(b, result, false)
	for i, d := range result.Days {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(b, "%s: %s\n", d.Date.Format("Mon, Jan 2"), d.Description)
		fmt.Fprintf(b, "  Temperature: %s to %s°C\n", formattedMetric(dayMetric(d, "min"), "%+.0f"), formattedMetric(dayMetric(d, "max"), "%+.0f"))
		for _, m := range []struct{ key, format string }{{"rain", "  Precipitation: %.1f mm\n"}, {"wind", "  Wind: %.0f km/h\n"}, {"uv", "  UV Index: %.0f\n"}} {
			if v := dayMetric(d, m.key); v != nil && (d.Values != nil || *v > 0) {
				fmt.Fprintf(b, m.format, *v)
			}
		}
		if d.Values == nil && d.Humidity > 0 {
			fmt.Fprintf(b, "  Humidity: %d%%\n", d.Humidity)
		}
		modelSample(b, d)
	}
	return b.String()
}

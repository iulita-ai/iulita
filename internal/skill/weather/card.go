package weather

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/i18n"
	"github.com/iulita-ai/iulita/internal/security"
)

func value[T any](v T) *T { return &v }
func validMetric(v *float64, lower, upper float64) *float64 {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) || *v < lower || *v > upper {
		return nil
	}
	return value(*v)
}
func stringMetric(raw string, lower, upper float64) *float64 {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return nil
	}
	return validMetric(&v, lower, upper)
}
func metricAt(s []*float64, i int, lower, upper float64) *float64 {
	if i >= len(s) {
		return nil
	}
	return validMetric(s[i], lower, upper)
}
func codeAt(s []*int, i int) *int {
	if i >= len(s) {
		return nil
	}
	return s[i]
}
func wmoCondition(code *int) domain.WeatherCondition {
	if code == nil {
		return domain.WeatherUnknown
	}
	switch *code {
	case 0, 1:
		return domain.WeatherClear
	case 2:
		return domain.WeatherPartlyCloudy
	case 3:
		return domain.WeatherCloudy
	case 45, 48:
		return domain.WeatherFog
	case 51, 53, 55, 56, 57, 61, 63, 65, 66, 67, 80, 81, 82:
		return domain.WeatherRain
	case 71, 73, 75, 77, 85, 86:
		return domain.WeatherSnow
	case 95, 96, 99:
		return domain.WeatherThunderstorm
	default:
		return domain.WeatherUnknown
	}
}
func owmCondition(code int) domain.WeatherCondition {
	switch code {
	case 200, 201, 202, 210, 211, 212, 221, 230, 231, 232:
		return domain.WeatherThunderstorm
	case 300, 301, 302, 310, 311, 312, 313, 314, 321, 500, 501, 502, 503, 504, 511, 520, 521, 522, 531:
		return domain.WeatherRain
	case 600, 601, 602, 611, 612, 613, 615, 616, 620, 621, 622:
		return domain.WeatherSnow
	case 701, 711, 721, 741:
		return domain.WeatherFog
	case 800:
		return domain.WeatherClear
	case 801, 802:
		return domain.WeatherPartlyCloudy
	case 803, 804:
		return domain.WeatherCloudy
	default:
		return domain.WeatherUnknown
	}
}
func wttrCondition(raw string) domain.WeatherCondition {
	code, err := strconv.Atoi(raw)
	if err != nil {
		return domain.WeatherUnknown
	}
	switch code {
	case 113:
		return domain.WeatherClear
	case 116:
		return domain.WeatherPartlyCloudy
	case 119, 122:
		return domain.WeatherCloudy
	case 143, 248, 260:
		return domain.WeatherFog
	case 176, 182, 185, 263, 266, 281, 284, 293, 296, 299, 302, 305, 308, 311, 314, 353, 356, 359, 362, 365:
		return domain.WeatherRain
	case 179, 227, 230, 317, 320, 323, 326, 329, 332, 335, 338, 350, 368, 371, 374, 377:
		return domain.WeatherSnow
	case 200, 386, 389, 392, 395:
		return domain.WeatherThunderstorm
	default:
		return domain.WeatherUnknown
	}
}
func legacyDay(d domain.WeatherDay, description string) DayForecast {
	date, parseErr := time.Parse("2006-01-02", d.Date)
	if parseErr != nil {
		// Normalized dates are validated upstream; preserve the unavailable date on invalid input.
		date = time.Time{}
	}
	out := DayForecast{Values: &d, Date: date, Description: description}
	if d.TempMinC != nil {
		out.TempMinC = *d.TempMinC
	}
	if d.TempMaxC != nil {
		out.TempMaxC = *d.TempMaxC
	}
	if d.PrecipitationMM != nil {
		out.PrecipMM = *d.PrecipitationMM
	}
	if d.WindKph != nil {
		out.WindKph = *d.WindKph
	}
	if d.UVIndexMax != nil {
		out.UVIndex = *d.UVIndexMax
	}
	return out
}
func validDate(raw string) bool {
	d, err := time.Parse("2006-01-02", raw)
	return err == nil && d.Format("2006-01-02") == raw
}
func validateBounds(d *domain.WeatherDay) {
	if d.TempMinC != nil && d.TempMaxC != nil && *d.TempMinC > *d.TempMaxC {
		d.TempMinC = nil
		d.TempMaxC = nil
	}
}
func displayLocation(requested, resolved string) string {
	if _, _, pin := parseCoordinatePair(requested); pin {
		return sharedPinDescriptor
	}
	if _, _, pin := parseCoordinatePair(resolved); pin {
		return sharedPinDescriptor
	}
	safe := security.RedactSensitiveText(strings.TrimSpace(resolved))
	safe = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, safe)
	runes := []rune(safe)
	if len(runes) > 160 {
		safe = string(runes[:160])
	}
	if safe == "" {
		safe = "Selected location"
	}
	return safe
}
func safeDescription(raw string, condition domain.WeatherCondition) string {
	text := security.RedactSensitiveText(raw)
	text = coordPairRe.ReplaceAllString(text, "[coordinates omitted]")
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, text)
	runes := []rune(strings.TrimSpace(text))
	if len(runes) > 160 {
		runes = runes[:160]
	}
	if len(runes) == 0 {
		return conditionText(context.Background(), condition)
	}
	return string(runes)
}
func weatherCard(result *WeatherResult, requested string, days int) *domain.WeatherCard {
	card := &domain.WeatherCard{Location: displayLocation(requested, result.Location), Source: result.Source, Timezone: result.Timezone, RetrievedAt: result.RetrievedAt, RequestedDays: days, Current: result.Current}
	for _, d := range result.Days {
		if d.Values != nil {
			card.Days = append(card.Days, *d.Values)
			continue
		}
		// Legacy injected backends have explicit scalar values, no real current data.
		card.Days = append(card.Days, domain.WeatherDay{Date: d.Date.Format("2006-01-02"), TempMinC: validMetric(&d.TempMinC, -150, 100), TempMaxC: validMetric(&d.TempMaxC, -150, 100), Condition: domain.WeatherUnknown})
	}
	return card.Clone()
}
func forecastDays(days int) int {
	if days < 1 {
		return 1
	}
	if days > 16 {
		return 16
	}
	return days
}
func (s *Skill) fetchForecast(ctx context.Context, location string, days int) (*WeatherResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(location) == "" {
		return nil, fmt.Errorf("explicit weather location required")
	}
	result, err := s.buildBackendChain().Fetch(ctx, strings.TrimSpace(location), forecastDays(days))
	if err != nil {
		return nil, err
	}
	// Provider names and location hints remain data, not authority or raw pins.
	result.Location = displayLocation(location, result.Location)
	return result, nil
}

// ForecastCard fetches once, without memory/IP inference or model-generated HTML.
func (s *Skill) ForecastCard(ctx context.Context, location string, days int) (*domain.WeatherCard, error) {
	channel.SetWeatherCard(ctx, nil)
	result, err := s.fetchForecast(ctx, location, days)
	if err != nil {
		return nil, err
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	card := weatherCard(result, location, forecastDays(days))
	channel.SetWeatherCard(ctx, card)
	return card, nil
}
func cardText(ctx context.Context, en, ru string) string {
	if strings.HasPrefix(i18n.LocaleFrom(ctx).String(), "ru") {
		return ru
	}
	return en
}
func conditionText(ctx context.Context, c domain.WeatherCondition) string {
	switch c {
	case domain.WeatherClear:
		return cardText(ctx, "Clear", "Ясно")
	case domain.WeatherPartlyCloudy:
		return cardText(ctx, "Partly cloudy", "Переменная облачность")
	case domain.WeatherCloudy:
		return cardText(ctx, "Cloudy", "Облачно")
	case domain.WeatherRain:
		return cardText(ctx, "Rain", "Дождь")
	case domain.WeatherSnow:
		return cardText(ctx, "Snow", "Снег")
	case domain.WeatherThunderstorm:
		return cardText(ctx, "Thunderstorm", "Гроза")
	case domain.WeatherFog:
		return cardText(ctx, "Fog", "Туман")
	default:
		return cardText(ctx, "Conditions unavailable", "Условия недоступны")
	}
}
func temperature(v *float64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%+.0f°C", *v)
}

// FormatCardText is a human-facing full forecast; unlike the skill's legacy
// model text it contains no instructions, markup, coordinates or invented data.
func FormatCardText(ctx context.Context, card *domain.WeatherCard) string {
	if card == nil {
		return cardText(ctx, "Weather unavailable", "Погода недоступна")
	}
	var b strings.Builder
	location := card.Location
	if location == sharedPinDescriptor {
		location = cardText(ctx, "The place you shared", "Место, которым вы поделились")
	}
	fmt.Fprintf(&b, "%s · %s\n", cardText(ctx, "Weather", "Погода"), location)
	if c := card.Current; c != nil {
		fmt.Fprintf(&b, "%s: %s · %s\n", cardText(ctx, "Current model conditions", "Текущая погода по модели"), temperature(c.TemperatureC), conditionText(ctx, c.Condition))
		if c.FeelsLikeC != nil {
			fmt.Fprintf(&b, "%s: %s\n", cardText(ctx, "Feels like", "Ощущается как"), temperature(c.FeelsLikeC))
		}
		if c.HumidityPercent != nil {
			fmt.Fprintf(&b, "%s: %d%%\n", cardText(ctx, "Humidity", "Влажность"), *c.HumidityPercent)
		}
		if c.WindKph != nil {
			fmt.Fprintf(&b, "%s: %.0f %s\n", cardText(ctx, "Wind", "Ветер"), *c.WindKph, cardText(ctx, "km/h", "км/ч"))
		}
		if c.PrecipitationMM != nil {
			period := cardText(ctx, "model interval", "интервал модели")
			if c.IntervalSeconds > 0 {
				period = fmt.Sprintf("%d %s", c.IntervalSeconds/60, cardText(ctx, "min", "мин"))
			}
			fmt.Fprintf(&b, "%s (%s): %.1f %s\n", cardText(ctx, "Precipitation", "Осадки"), period, *c.PrecipitationMM, cardText(ctx, "mm", "мм"))
		}
		if !c.ObservedAt.IsZero() {
			fmt.Fprintf(&b, "%s: %s\n", cardText(ctx, "Current data time", "Время текущих данных"), c.ObservedAt.Format("2006-01-02 15:04 MST"))
		}
	} else {
		b.WriteString(cardText(ctx, "Forecast only; current conditions unavailable.\n", "Только прогноз; текущие данные недоступны.\n"))
	}
	for _, d := range card.Days {
		fmt.Fprintf(&b, "\n%s · %s\n%s: %s … %s\n", d.Date, conditionText(ctx, d.Condition), cardText(ctx, "Temperature", "Температура"), temperature(d.TempMinC), temperature(d.TempMaxC))
		if d.PrecipitationMM != nil {
			fmt.Fprintf(&b, "%s: %.1f %s\n", cardText(ctx, "Daily precipitation", "Осадки за день"), *d.PrecipitationMM, cardText(ctx, "mm", "мм"))
		}
		if d.WindKph != nil {
			fmt.Fprintf(&b, "%s: %.0f %s\n", cardText(ctx, "Maximum wind", "Максимальный ветер"), *d.WindKph, cardText(ctx, "km/h", "км/ч"))
		}
		if d.UVIndexMax != nil {
			fmt.Fprintf(&b, "%s: %.0f\n", cardText(ctx, "Maximum UV index", "Максимальный УФ-индекс"), *d.UVIndexMax)
		}
	}
	if len(card.Days) < card.RequestedDays {
		fmt.Fprintf(&b, "\n%s: %d/%d %s\n", cardText(ctx, "Available forecast", "Доступный прогноз"), len(card.Days), card.RequestedDays, cardText(ctx, "days", "дней"))
	}
	fmt.Fprintf(&b, "\n%s: %s", cardText(ctx, "Source", "Источник"), card.Source)
	if !card.RetrievedAt.IsZero() {
		fmt.Fprintf(&b, " · %s %s", cardText(ctx, "received", "получено"), card.RetrievedAt.Format("2006-01-02 15:04 MST"))
	}
	return b.String()
}

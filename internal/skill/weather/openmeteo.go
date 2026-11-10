package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/domain"
)

const (
	nominatimURL = "https://geocoding-api.open-meteo.com/v1/search"
	openMeteoURL = "https://api.open-meteo.com/v1/forecast"
	userAgent    = "iulita-bot/1.0"
	maxRespSize  = 128 * 1024 // 128 KB
)

// OpenMeteoBackend uses Open-Meteo (free, no API key).
type OpenMeteoBackend struct {
	client *http.Client
}

func NewOpenMeteoBackend(client *http.Client) *OpenMeteoBackend {
	return &OpenMeteoBackend{client: client}
}

func (b *OpenMeteoBackend) Name() string { return "open-meteo" }

func (b *OpenMeteoBackend) Fetch(ctx context.Context, location string, days int) (*WeatherResult, error) {
	// Geocode location name to coordinates.
	lat, lon, resolvedName, err := b.geocode(ctx, location)
	if err != nil {
		return nil, fmt.Errorf("geocoding %q: %w", location, err)
	}

	// Fetch forecast.
	return b.fetchForecast(ctx, lat, lon, resolvedName, days)
}

func (b *OpenMeteoBackend) geocode(ctx context.Context, location string) (lat, lon float64, name string, err error) {
	// Coordinate strings ("52.516270, 13.377750" — e.g. a shared map pin) are
	// already resolved: skip the name-search API, which cannot geocode numbers.
	if la, lo, ok := parseCoordinatePair(location); ok {
		// Coordinate-free display name: formatMarkdown special-cases this
		// sentinel so it is never demanded verbatim (it is not a city name,
		// and verbatim English would leak into localized replies).
		return la, lo, sharedPinDescriptor, nil
	}

	// Detect language: use "ru" for Cyrillic input, "en" otherwise.
	lang := "en"
	for _, r := range location {
		if r >= 0x0400 && r <= 0x04FF { // Cyrillic Unicode block
			lang = "ru"
			break
		}
	}
	u := fmt.Sprintf("%s?name=%s&count=5&language=%s&format=json", nominatimURL, url.QueryEscape(location), lang)
	body, err := b.httpGet(ctx, u)
	if err != nil {
		return 0, 0, "", err
	}

	var resp struct {
		Results []struct {
			Name       string   `json:"name"`
			Country    string   `json:"country"`
			Admin1     string   `json:"admin1"`
			Latitude   *float64 `json:"latitude"`
			Longitude  *float64 `json:"longitude"`
			Population int      `json:"population"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, 0, "", fmt.Errorf("parse geocoding response: %w", err)
	}
	if len(resp.Results) == 0 {
		return 0, 0, "", fmt.Errorf("location %q not found", location)
	}

	// Pick the result with the highest population (most likely the intended city).
	best := -1
	for i, r := range resp.Results {
		if r.Latitude == nil || r.Longitude == nil || channel.ValidateCoords(*r.Latitude, *r.Longitude) != nil {
			continue
		}
		if best < 0 || r.Population > resp.Results[best].Population {
			best = i
		}
	}
	if best < 0 {
		return 0, 0, "", fmt.Errorf("geocoding returned no valid coordinates")
	}
	r := resp.Results[best]
	resolvedName := r.Name
	if r.Admin1 != "" {
		resolvedName += ", " + r.Admin1
	}
	if r.Country != "" {
		resolvedName += ", " + r.Country
	}
	return *r.Latitude, *r.Longitude, resolvedName, nil
}

// sharedPinDescriptor is the display name used for coordinate lookups (a
// shared map pin) — never demanded verbatim by the formatter.
const sharedPinDescriptor = "the location you shared"

// coordPairRe matches a decimal coordinate pair wherever it sits — plain
// ("52.516270, 13.377750"), parenthesized in a venue marker ("[Place]: Title —
// Address (52.516270, 13.377750)"), or followed by a suffix ("(±35 m)").
var coordPairRe = regexp.MustCompile(`(-?\d{1,3}\.\d+)\s*,\s*(-?\d{1,3}\.\d+)`)

// parseCoordinatePair extracts a "lat, lon" pair from a string the model may
// have copied from a shared-pin/venue marker line, and validates it through
// the shared typed boundary (channel.ValidateCoords). Unicode format
// characters (LRM/RLM marks injected for RTL locales) are stripped first —
// they are invisible but break strconv.ParseFloat.
func parseCoordinatePair(s string) (lat, lon float64, ok bool) {
	clean := strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1 // drop format chars (LRM U+200E, RLM U+200F, …)
		}
		return r
	}, s)
	// Take the LAST match: the marker grammar always places the true
	// coordinates at the end of the line, while a decoy decimal pair (e.g. a
	// venue title "Room 4.5, 6.2 Hostel") can only appear earlier.
	matches := coordPairRe.FindAllStringSubmatch(strings.TrimSpace(clean), -1)
	if len(matches) == 0 {
		return 0, 0, false
	}
	m := matches[len(matches)-1]
	la, err1 := strconv.ParseFloat(m[1], 64)
	lo, err2 := strconv.ParseFloat(m[2], 64)
	if err1 != nil || err2 != nil || channel.ValidateCoords(la, lo) != nil {
		return 0, 0, false
	}
	return la, lo, true
}

func (b *OpenMeteoBackend) fetchForecast(ctx context.Context, lat, lon float64, location string, days int) (*WeatherResult, error) {
	days = forecastDays(days)
	u := fmt.Sprintf("%s?latitude=%.4f&longitude=%.4f&current=temperature_2m,relative_humidity_2m,apparent_temperature,is_day,weather_code,wind_speed_10m,precipitation&daily=weather_code,temperature_2m_max,temperature_2m_min,precipitation_sum,wind_speed_10m_max,uv_index_max&temperature_unit=celsius&wind_speed_unit=kmh&precipitation_unit=mm&timezone=auto&forecast_days=%d", openMeteoURL, lat, lon, days)
	body, err := b.httpGet(ctx, u)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Timezone         string            `json:"timezone"`
		UTCOffsetSeconds *int              `json:"utc_offset_seconds"`
		CurrentUnits     map[string]string `json:"current_units"`
		DailyUnits       map[string]string `json:"daily_units"`
		Current          *struct {
			Time          string   `json:"time"`
			Interval      int      `json:"interval"`
			Temperature   *float64 `json:"temperature_2m"`
			FeelsLike     *float64 `json:"apparent_temperature"`
			Humidity      *int     `json:"relative_humidity_2m"`
			IsDay         *int     `json:"is_day"`
			Code          *int     `json:"weather_code"`
			Wind          *float64 `json:"wind_speed_10m"`
			Precipitation *float64 `json:"precipitation"`
		} `json:"current"`
		Daily struct {
			Time    []string   `json:"time"`
			Code    []*int     `json:"weather_code"`
			TempMax []*float64 `json:"temperature_2m_max"`
			TempMin []*float64 `json:"temperature_2m_min"`
			Precip  []*float64 `json:"precipitation_sum"`
			Wind    []*float64 `json:"wind_speed_10m_max"`
			UV      []*float64 `json:"uv_index_max"`
		} `json:"daily"`
	}
	if err = json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse forecast: %w", err)
	}
	result := &WeatherResult{Location: location, Source: "open-meteo", RetrievedAt: time.Now().UTC()}
	var tz *time.Location
	if resp.Timezone != "" {
		if parsed, e := time.LoadLocation(resp.Timezone); e == nil {
			tz = parsed
			result.Timezone = resp.Timezone
		}
	}
	if resp.UTCOffsetSeconds != nil && *resp.UTCOffsetSeconds >= -14*3600 && *resp.UTCOffsetSeconds <= 14*3600 {
		tz = time.FixedZone(result.Timezone, *resp.UTCOffsetSeconds)
	}
	if c := resp.Current; c != nil {
		at, e := time.Parse(time.RFC3339, c.Time)
		if e != nil && tz != nil {
			at, e = time.ParseInLocation("2006-01-02T15:04", c.Time, tz)
		}
		if e == nil && !at.IsZero() {
			current := &domain.WeatherCurrent{ObservedAt: at, Condition: wmoCondition(c.Code)}
			if unitOK(resp.CurrentUnits, "temperature_2m", "°C") {
				current.TemperatureC = validMetric(c.Temperature, -150, 100)
			}
			if unitOK(resp.CurrentUnits, "apparent_temperature", "°C") {
				current.FeelsLikeC = validMetric(c.FeelsLike, -200, 150)
			}
			if unitOK(resp.CurrentUnits, "relative_humidity_2m", "%") && c.Humidity != nil && *c.Humidity >= 0 && *c.Humidity <= 100 {
				current.HumidityPercent = value(*c.Humidity)
			}
			if unitOK(resp.CurrentUnits, "wind_speed_10m", "km/h") {
				current.WindKph = validMetric(c.Wind, 0, 500)
			}
			if unitOK(resp.CurrentUnits, "precipitation", "mm") {
				current.PrecipitationMM = validMetric(c.Precipitation, 0, 10000)
			}
			if c.IsDay != nil && (*c.IsDay == 0 || *c.IsDay == 1) {
				current.IsDay = value(*c.IsDay == 1)
			}
			if c.Interval > 0 && c.Interval <= 86400 {
				current.IntervalSeconds = c.Interval
			}
			// Untimed/entirely unavailable current payloads never get a hero reading.
			if current.TemperatureC != nil || current.FeelsLikeC != nil || current.WindKph != nil || current.HumidityPercent != nil || current.PrecipitationMM != nil || current.Condition != domain.WeatherUnknown {
				result.Current = current
			}
		}
	}
	for i, date := range resp.Daily.Time {
		if len(result.Days) >= days {
			break
		}
		if !validDate(date) {
			continue
		}
		d := domain.WeatherDay{Date: date, Condition: wmoCondition(codeAt(resp.Daily.Code, i))}
		if unitOK(resp.DailyUnits, "temperature_2m_min", "°C") {
			d.TempMinC = metricAt(resp.Daily.TempMin, i, -150, 100)
		}
		if unitOK(resp.DailyUnits, "temperature_2m_max", "°C") {
			d.TempMaxC = metricAt(resp.Daily.TempMax, i, -150, 100)
		}
		if unitOK(resp.DailyUnits, "precipitation_sum", "mm") {
			d.PrecipitationMM = metricAt(resp.Daily.Precip, i, 0, 10000)
		}
		if unitOK(resp.DailyUnits, "wind_speed_10m_max", "km/h") {
			d.WindKph = metricAt(resp.Daily.Wind, i, 0, 500)
		}
		d.UVIndexMax = metricAt(resp.Daily.UV, i, 0, 100)
		validateBounds(&d)
		description := "Conditions unavailable"
		if code := codeAt(resp.Daily.Code, i); code != nil && d.Condition != domain.WeatherUnknown {
			description = WMODescription(*code)
		}
		result.Days = append(result.Days, legacyDay(d, description))
	}
	if len(result.Days) == 0 {
		return nil, fmt.Errorf("Open-Meteo returned no valid forecast days")
	}
	return result, nil
}
func unitOK(units map[string]string, key, want string) bool {
	return units[key] == "" || units[key] == want
}

func (b *OpenMeteoBackend) httpGet(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRespSize))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}

func safeIdx(s []int, i int) int {
	if i < len(s) {
		return s[i]
	}
	return 0
}

func safeIdxF(s []float64, i int) float64 {
	if i < len(s) {
		return s[i]
	}
	return 0
}

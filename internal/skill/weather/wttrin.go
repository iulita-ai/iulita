package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
)

const wttrInURL = "https://wttr.in/"

// WttrInBackend uses wttr.in (free, no API key).
type WttrInBackend struct {
	client *http.Client
}

func NewWttrInBackend(client *http.Client) *WttrInBackend {
	return &WttrInBackend{client: client}
}

func (b *WttrInBackend) Name() string { return "wttr.in" }

func (b *WttrInBackend) Fetch(ctx context.Context, location string, days int) (*WeatherResult, error) {
	if days < 1 {
		days = 1
	}
	if days > 3 {
		days = 3 // wttr.in supports max 3 days
	}

	u := fmt.Sprintf("%s%s?format=j1", wttrInURL, url.PathEscape(location))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
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

	var data wttrResponse
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("parse wttr.in response: %w", err)
	}

	resolvedLocation := location
	if len(data.NearestArea) > 0 {
		area := data.NearestArea[0]
		name := firstValue(area.AreaName)
		country := firstValue(area.Country)
		if name != "" {
			resolvedLocation = name
			if country != "" {
				resolvedLocation += ", " + country
			}
		}
	}

	result := &WeatherResult{Location: displayLocation(location, resolvedLocation), Source: "wttr.in", RetrievedAt: time.Now().UTC()}
	for i, w := range data.Weather {
		if i >= days {
			break
		}
		if !validDate(w.Date) {
			continue
		}
		d := domain.WeatherDay{Date: w.Date, TempMaxC: stringMetric(w.MaxTempC, -150, 100), TempMinC: stringMetric(w.MinTempC, -150, 100), UVIndexMax: nil, Condition: domain.WeatherUnknown}
		description := "Conditions unavailable"
		if len(w.Hourly) > 0 {
			h := w.Hourly[len(w.Hourly)/2]
			d.Condition = wttrCondition(h.WeatherCode)
			// Midday samples are not authoritative daily sums/maxima or current data.
			// Only min/max temperature and the known forecast condition enter the card.
			if d.Condition != domain.WeatherUnknown {
				description = safeDescription(firstValue(h.WeatherDesc), d.Condition)
			}
		}
		validateBounds(&d)
		legacy := legacyDay(d, description)
		sample := &ForecastSample{Kind: "Midday forecast sample", UVIndex: stringMetric(w.UVIndex, 0, 100)}
		if len(w.Hourly) > 0 {
			h := w.Hourly[len(w.Hourly)/2]
			sample.WindKph = stringMetric(h.WindSpeedKmph, 0, 500)
			sample.PrecipitationMM = stringMetric(h.PrecipMM, 0, 10000)
			if n, e := strconv.Atoi(h.Humidity); e == nil && n >= 0 && n <= 100 {
				sample.HumidityPercent = value(n)
			}
		}
		legacy.Sample = sample
		// Preserve the legacy model's supplied daily UV and midday samples, with
		// explicit sample labels in the formatter; never promote them into Current.
		if uv := stringMetric(w.UVIndex, 0, 100); uv != nil {
			legacy.UVIndex = *uv
		}
		result.Days = append(result.Days, legacy)
	}

	if len(result.Days) == 0 {
		return nil, fmt.Errorf("wttr.in returned no weather data")
	}

	return result, nil
}

type wttrResponse struct {
	Weather     []wttrWeather     `json:"weather"`
	NearestArea []wttrNearestArea `json:"nearest_area"`
}

type wttrWeather struct {
	Date     string       `json:"date"`
	MaxTempC string       `json:"maxtempC"`
	MinTempC string       `json:"mintempC"`
	UVIndex  string       `json:"uvIndex"`
	Hourly   []wttrHourly `json:"hourly"`
}

type wttrHourly struct {
	WeatherCode   string      `json:"weatherCode"`
	WeatherDesc   []wttrValue `json:"weatherDesc"`
	WindSpeedKmph string      `json:"windspeedKmph"`
	PrecipMM      string      `json:"precipMM"`
	Humidity      string      `json:"humidity"`
}

type wttrNearestArea struct {
	AreaName []wttrValue `json:"areaName"`
	Country  []wttrValue `json:"country"`
}

type wttrValue struct {
	Value string `json:"value"`
}

func firstValue(vals []wttrValue) string {
	if len(vals) > 0 {
		return vals[0].Value
	}
	return ""
}

func parseFloat(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func parseInt(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

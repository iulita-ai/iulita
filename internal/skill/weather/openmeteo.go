package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
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
			Name       string  `json:"name"`
			Country    string  `json:"country"`
			Admin1     string  `json:"admin1"`
			Latitude   float64 `json:"latitude"`
			Longitude  float64 `json:"longitude"`
			Population int     `json:"population"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, 0, "", fmt.Errorf("parse geocoding response: %w", err)
	}
	if len(resp.Results) == 0 {
		return 0, 0, "", fmt.Errorf("location %q not found", location)
	}

	// Pick the result with the highest population (most likely the intended city).
	best := 0
	for i, r := range resp.Results {
		if r.Population > resp.Results[best].Population {
			best = i
		}
	}
	r := resp.Results[best]
	resolvedName := r.Name
	if r.Admin1 != "" {
		resolvedName += ", " + r.Admin1
	}
	if r.Country != "" {
		resolvedName += ", " + r.Country
	}
	return r.Latitude, r.Longitude, resolvedName, nil
}

func (b *OpenMeteoBackend) fetchForecast(ctx context.Context, lat, lon float64, location string, days int) (*WeatherResult, error) {
	if days < 1 {
		days = 1
	}
	if days > 16 {
		days = 16
	}

	u := fmt.Sprintf(
		"%s?latitude=%.4f&longitude=%.4f&daily=weather_code,temperature_2m_max,temperature_2m_min,precipitation_sum,wind_speed_10m_max,uv_index_max&timezone=auto&forecast_days=%d",
		openMeteoURL, lat, lon, days,
	)
	body, err := b.httpGet(ctx, u)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Daily struct {
			Time         []string  `json:"time"`
			WeatherCode  []int     `json:"weather_code"`
			TempMax      []float64 `json:"temperature_2m_max"`
			TempMin      []float64 `json:"temperature_2m_min"`
			PrecipSum    []float64 `json:"precipitation_sum"`
			WindSpeedMax []float64 `json:"wind_speed_10m_max"`
			UVIndexMax   []float64 `json:"uv_index_max"`
		} `json:"daily"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse forecast: %w", err)
	}

	d := resp.Daily
	result := &WeatherResult{Location: location}
	for i := range d.Time {
		date, _ := time.Parse("2006-01-02", d.Time[i])
		forecast := DayForecast{
			Date:        date,
			Description: WMODescription(safeIdx(d.WeatherCode, i)),
			TempMaxC:    safeIdxF(d.TempMax, i),
			TempMinC:    safeIdxF(d.TempMin, i),
			PrecipMM:    safeIdxF(d.PrecipSum, i),
			WindKph:     safeIdxF(d.WindSpeedMax, i),
			UVIndex:     safeIdxF(d.UVIndexMax, i),
		}
		result.Days = append(result.Days, forecast)
	}

	return result, nil
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

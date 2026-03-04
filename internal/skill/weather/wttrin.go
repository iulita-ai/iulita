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

	result := &WeatherResult{Location: resolvedLocation}
	for i, w := range data.Weather {
		if i >= days {
			break
		}
		date, _ := time.Parse("2006-01-02", w.Date)
		forecast := DayForecast{
			Date:     date,
			TempMaxC: parseFloat(w.MaxTempC),
			TempMinC: parseFloat(w.MinTempC),
			UVIndex:  parseFloat(w.UVIndex),
		}
		if len(w.Hourly) > 0 {
			h := w.Hourly[len(w.Hourly)/2] // mid-day hourly entry
			forecast.Description = firstValue(h.WeatherDesc)
			forecast.WindKph = parseFloat(h.WindSpeedKmph)
			forecast.PrecipMM = parseFloat(h.PrecipMM)
			forecast.Humidity = parseInt(h.Humidity)
		}
		result.Days = append(result.Days, forecast)
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

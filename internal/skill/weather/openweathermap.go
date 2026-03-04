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

const owmBaseURL = "https://api.openweathermap.org/data/2.5/forecast"

// OWMBackend uses OpenWeatherMap (requires API key).
type OWMBackend struct {
	client *http.Client
	apiKey string
}

func NewOWMBackend(client *http.Client, apiKey string) *OWMBackend {
	return &OWMBackend{client: client, apiKey: apiKey}
}

func (b *OWMBackend) Name() string { return "openweathermap" }

func (b *OWMBackend) Fetch(ctx context.Context, location string, days int) (*WeatherResult, error) {
	if b.apiKey == "" {
		return nil, fmt.Errorf("OpenWeatherMap API key not configured")
	}
	if days < 1 {
		days = 1
	}
	if days > 5 {
		days = 5 // free tier limit
	}

	u := fmt.Sprintf("%s?q=%s&appid=%s&units=metric&cnt=%d",
		owmBaseURL, url.QueryEscape(location), b.apiKey, days*8)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)

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

	var data owmResponse
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("parse OWM response: %w", err)
	}

	resolvedLocation := location
	if data.City.Name != "" {
		resolvedLocation = data.City.Name
		if data.City.Country != "" {
			resolvedLocation += ", " + data.City.Country
		}
	}

	// Aggregate 3-hour entries into daily forecasts.
	dayMap := make(map[string]*DayForecast)
	var dayOrder []string
	for _, entry := range data.List {
		date := time.Unix(entry.Dt, 0).Format("2006-01-02")
		if _, exists := dayMap[date]; !exists {
			t, _ := time.Parse("2006-01-02", date)
			dayMap[date] = &DayForecast{
				Date:     t,
				TempMinC: entry.Main.TempMin,
				TempMaxC: entry.Main.TempMax,
			}
			dayOrder = append(dayOrder, date)
		}
		d := dayMap[date]
		if entry.Main.TempMin < d.TempMinC {
			d.TempMinC = entry.Main.TempMin
		}
		if entry.Main.TempMax > d.TempMaxC {
			d.TempMaxC = entry.Main.TempMax
		}
		d.PrecipMM += entry.Rain.ThreeH + entry.Snow.ThreeH
		if entry.Wind.Speed*3.6 > d.WindKph {
			d.WindKph = entry.Wind.Speed * 3.6 // m/s → km/h
		}
		if entry.Main.Humidity > d.Humidity {
			d.Humidity = entry.Main.Humidity
		}
		if len(entry.Weather) > 0 {
			d.Description = entry.Weather[0].Description
		}
	}

	result := &WeatherResult{Location: resolvedLocation}
	for i, date := range dayOrder {
		if i >= days {
			break
		}
		result.Days = append(result.Days, *dayMap[date])
	}

	if len(result.Days) == 0 {
		return nil, fmt.Errorf("OWM returned no data for %q", location)
	}

	return result, nil
}

type owmResponse struct {
	City struct {
		Name    string `json:"name"`
		Country string `json:"country"`
	} `json:"city"`
	List []owmEntry `json:"list"`
}

type owmEntry struct {
	Dt   int64 `json:"dt"`
	Main struct {
		TempMin  float64 `json:"temp_min"`
		TempMax  float64 `json:"temp_max"`
		Humidity int     `json:"humidity"`
	} `json:"main"`
	Weather []struct {
		Description string `json:"description"`
	} `json:"weather"`
	Wind struct {
		Speed float64 `json:"speed"`
	} `json:"wind"`
	Rain struct {
		ThreeH float64 `json:"3h"`
	} `json:"rain"`
	Snow struct {
		ThreeH float64 `json:"3h"`
	} `json:"snow"`
}

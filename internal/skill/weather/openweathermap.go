package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
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

	// Forecast buckets are dated in the city's offset, never the host timezone.
	if data.City.Timezone == nil || *data.City.Timezone < -14*3600 || *data.City.Timezone > 14*3600 {
		return nil, fmt.Errorf("OWM forecast timezone unavailable")
	}
	zone := time.FixedZone("", *data.City.Timezone)
	sort.Slice(data.List, func(i, j int) bool { return data.List[i].Dt < data.List[j].Dt })
	result := &WeatherResult{Location: displayLocation(location, resolvedLocation), Source: "openweathermap", RetrievedAt: time.Now().UTC()}
	if data.City.Timezone != nil {
		result.Timezone = time.Now().In(zone).Format("-07:00")
	}
	dayMap := map[string]*domain.WeatherDay{}
	samples := map[string]*ForecastSample{}
	descriptions := map[string]string{}
	dayOrder := []string{}
	for _, entry := range data.List {
		if entry.Dt <= 0 {
			continue
		}
		date := time.Unix(entry.Dt, 0).In(zone).Format("2006-01-02")
		d := dayMap[date]
		if d == nil {
			d = &domain.WeatherDay{Date: date, Condition: domain.WeatherUnknown}
			dayMap[date] = d
			dayOrder = append(dayOrder, date)
			samples[date] = &ForecastSample{Kind: "Available three-hour forecast periods"}
		}
		if v := validMetric(entry.Main.TempMin, -150, 100); v != nil && (d.TempMinC == nil || *v < *d.TempMinC) {
			d.TempMinC = v
		}
		if v := validMetric(entry.Main.TempMax, -150, 100); v != nil && (d.TempMaxC == nil || *v > *d.TempMaxC) {
			d.TempMaxC = v
		}
		if speed := validMetric(entry.Wind.Speed, 0, 140); speed != nil {
			v := *speed * 3.6
			if d.WindKph == nil || v > *d.WindKph {
				d.WindKph = value(v)
			}
		}
		// Rain/snow are 3-hour forecasts. Only aggregate present fields, never
		// fabricate a measured dry day from absent records.
		for _, v := range []*float64{entry.Rain.ThreeH, entry.Snow.ThreeH} {
			if rain := validMetric(v, 0, 10000); rain != nil {
				if samples[date].PrecipitationMM == nil {
					samples[date].PrecipitationMM = value(0.0)
				}
				*samples[date].PrecipitationMM += *rain
			}
		}
		if h := entry.Main.Humidity; h != nil && *h >= 0 && *h <= 100 && (samples[date].HumidityPercent == nil || *h > *samples[date].HumidityPercent) {
			samples[date].HumidityPercent = value(*h)
		}
		if len(entry.Weather) > 0 {
			c := owmCondition(entry.Weather[0].ID)
			if c != domain.WeatherUnknown {
				d.Condition = c
				descriptions[date] = safeDescription(entry.Weather[0].Description, c)
			}
		}
	}
	for _, date := range dayOrder {
		if len(result.Days) >= days {
			break
		}
		d := dayMap[date]
		validateBounds(d)
		description := descriptions[date]
		if description == "" {
			description = conditionText(context.Background(), d.Condition)
		}
		legacy := legacyDay(*d, description)
		legacy.Sample = samples[date]
		result.Days = append(result.Days, legacy)
	}

	if len(result.Days) == 0 {
		return nil, fmt.Errorf("OWM returned no data for %q", location)
	}

	return result, nil
}

type owmResponse struct {
	City struct {
		Name     string `json:"name"`
		Country  string `json:"country"`
		Timezone *int   `json:"timezone"`
	} `json:"city"`
	List []owmEntry `json:"list"`
}

type owmEntry struct {
	Dt   int64 `json:"dt"`
	Main struct {
		TempMin  *float64 `json:"temp_min"`
		TempMax  *float64 `json:"temp_max"`
		Humidity *int     `json:"humidity"`
	} `json:"main"`
	Weather []struct {
		ID          int    `json:"id"`
		Description string `json:"description"`
	} `json:"weather"`
	Wind struct {
		Speed *float64 `json:"speed"`
	} `json:"wind"`
	Rain struct {
		ThreeH *float64 `json:"3h"`
	} `json:"rain"`
	Snow struct {
		ThreeH *float64 `json:"3h"`
	} `json:"snow"`
}

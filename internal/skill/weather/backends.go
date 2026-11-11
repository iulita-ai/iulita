package weather

import (
	"context"
	"fmt"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
)

// WeatherBackend fetches weather forecasts for a location.
type WeatherBackend interface {
	Name() string
	Fetch(ctx context.Context, location string, days int) (*WeatherResult, error)
}

// WeatherResult holds normalized weather data.
type WeatherResult struct {
	Source      string
	Timezone    string
	RetrievedAt time.Time
	Current     *domain.WeatherCurrent
	Location    string        // resolved location name
	Days        []DayForecast // one entry per day
}

// DayForecast holds weather data for a single day.
type ForecastSample struct {
	Kind            string
	PrecipitationMM *float64
	WindKph         *float64
	HumidityPercent *int
	UVIndex         *float64
}
type DayForecast struct {
	Sample *ForecastSample
	// Values records source presence. Nil is reserved for legacy/test backends.
	Values      *domain.WeatherDay
	Date        time.Time
	TempMinC    float64
	TempMaxC    float64
	Description string  // e.g. "Partly cloudy", "Rain showers"
	PrecipMM    float64 // total precipitation in mm
	WindKph     float64 // max wind speed
	Humidity    int     // average humidity %
	UVIndex     float64
}

// fallbackChain tries backends in order until one succeeds.
type fallbackChain struct {
	backends []WeatherBackend
}

func newFallbackChain(backends ...WeatherBackend) WeatherBackend {
	return &fallbackChain{backends: backends}
}

func (fc *fallbackChain) Name() string {
	return "fallback"
}

func (fc *fallbackChain) Fetch(ctx context.Context, location string, days int) (*WeatherResult, error) {
	var lastErr error
	for _, b := range fc.backends {
		if err := CheckCommandGuard(ctx); err != nil {
			return nil, err
		}
		result, err := b.Fetch(ctx, location, days)
		if err == nil && !usefulForecast(result) {
			err = fmt.Errorf("empty forecast")
		}
		if err == nil {
			result.Source = b.Name()
			if result.RetrievedAt.IsZero() {
				result.RetrievedAt = time.Now().UTC()
			}
			return result, nil
		}
		lastErr = fmt.Errorf("%s: %w", b.Name(), err)
	}
	if lastErr != nil {
		return nil, fmt.Errorf("all weather backends failed: %w", lastErr)
	}
	return nil, fmt.Errorf("no weather backends configured")
}

func usefulForecast(r *WeatherResult) bool {
	if r == nil || len(r.Days) == 0 {
		return false
	}
	for _, d := range r.Days {
		if d.Values == nil {
			return true
		}
		v := d.Values
		if v.TempMinC != nil || v.TempMaxC != nil || v.PrecipitationMM != nil || v.WindKph != nil || v.UVIndexMax != nil || v.Condition != domain.WeatherUnknown {
			return true
		}
	}
	return false
}

package weather

import (
	"context"
	"fmt"
	"time"
)

// WeatherBackend fetches weather forecasts for a location.
type WeatherBackend interface {
	Name() string
	Fetch(ctx context.Context, location string, days int) (*WeatherResult, error)
}

// WeatherResult holds normalized weather data.
type WeatherResult struct {
	Location string        // resolved location name
	Days     []DayForecast // one entry per day
}

// DayForecast holds weather data for a single day.
type DayForecast struct {
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
		result, err := b.Fetch(ctx, location, days)
		if err == nil {
			return result, nil
		}
		lastErr = fmt.Errorf("%s: %w", b.Name(), err)
	}
	if lastErr != nil {
		return nil, fmt.Errorf("all weather backends failed: %w", lastErr)
	}
	return nil, fmt.Errorf("no weather backends configured")
}

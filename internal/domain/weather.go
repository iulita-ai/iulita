package domain

import "time"

// WeatherCondition selects only a locally defined illustration. Provider prose
// and model output are never interpreted as markup or illustration names.
type WeatherCondition string

// Weather condition constants select fixed local illustrations.
const (
	// WeatherUnknown selects a neutral illustration when source conditions are unavailable.
	WeatherUnknown      WeatherCondition = "unknown"
	WeatherClear        WeatherCondition = "clear"
	WeatherPartlyCloudy WeatherCondition = "partly_cloudy"
	WeatherCloudy       WeatherCondition = "cloudy"
	WeatherRain         WeatherCondition = "rain"
	WeatherSnow         WeatherCondition = "snow"
	WeatherThunderstorm WeatherCondition = "thunderstorm"
	WeatherFog          WeatherCondition = "fog"
)

// WeatherCard is transport-independent, factual presentation data. Nil metrics
// mean unavailable, while a pointer to zero is a real measured/model value.
// Location is a display name, never raw coordinates; Source is a known backend.
type WeatherCard struct {
	Location      string          `json:"location"`
	Source        string          `json:"source"`
	Timezone      string          `json:"timezone,omitempty"`
	RetrievedAt   time.Time       `json:"retrieved_at"`
	RequestedDays int             `json:"requested_days"`
	Current       *WeatherCurrent `json:"current,omitempty"`
	Days          []WeatherDay    `json:"days"`
}

// WeatherCurrent contains timed provider values with explicit metric availability.
type WeatherCurrent struct {
	ObservedAt      time.Time        `json:"observed_at"`
	IntervalSeconds int              `json:"interval_seconds,omitempty"`
	TemperatureC    *float64         `json:"temperature_c,omitempty"`
	FeelsLikeC      *float64         `json:"feels_like_c,omitempty"`
	HumidityPercent *int             `json:"humidity_percent,omitempty"`
	WindKph         *float64         `json:"wind_kph,omitempty"`
	PrecipitationMM *float64         `json:"precipitation_mm,omitempty"`
	Condition       WeatherCondition `json:"condition"`
	IsDay           *bool            `json:"is_day,omitempty"`
}

// WeatherDay contains date-local forecast values without inventing missing metrics.
type WeatherDay struct {
	Date            string           `json:"date"` // Local calendar date, not a UTC midnight instant.
	TempMinC        *float64         `json:"temp_min_c,omitempty"`
	TempMaxC        *float64         `json:"temp_max_c,omitempty"`
	PrecipitationMM *float64         `json:"precipitation_mm,omitempty"` // Daily sum only.
	WindKph         *float64         `json:"wind_kph,omitempty"`         // Daily maximum when available.
	UVIndexMax      *float64         `json:"uv_index_max,omitempty"`
	Condition       WeatherCondition `json:"condition"`
}

func cloneWeatherValue[T any](v *T) *T {
	if v == nil {
		return nil
	}
	snapshot := *v
	return &snapshot
}

// Clone prevents producer/consumer aliasing across concurrent request contexts.
func (c *WeatherCard) Clone() *WeatherCard {
	if c == nil {
		return nil
	}
	out := *c
	if c.Current != nil {
		v := *c.Current
		v.TemperatureC = cloneWeatherValue(v.TemperatureC)
		v.FeelsLikeC = cloneWeatherValue(v.FeelsLikeC)
		v.HumidityPercent = cloneWeatherValue(v.HumidityPercent)
		v.WindKph = cloneWeatherValue(v.WindKph)
		v.PrecipitationMM = cloneWeatherValue(v.PrecipitationMM)
		v.IsDay = cloneWeatherValue(v.IsDay)
		out.Current = &v
	}
	out.Days = make([]WeatherDay, len(c.Days))
	for i, v := range c.Days {
		v.TempMinC = cloneWeatherValue(v.TempMinC)
		v.TempMaxC = cloneWeatherValue(v.TempMaxC)
		v.PrecipitationMM = cloneWeatherValue(v.PrecipitationMM)
		v.WindKph = cloneWeatherValue(v.WindKph)
		v.UVIndexMax = cloneWeatherValue(v.UVIndexMax)
		out.Days[i] = v
	}
	return &out
}

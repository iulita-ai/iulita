package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/defaults"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/skill/interact"
	"github.com/iulita-ai/iulita/internal/storage"
)

// SkillLookup finds a skill by name. Implemented by skill.Registry.
type SkillLookup interface {
	Get(name string) (skill.Skill, bool)
}

// Skill provides weather forecasts with a deterministic fallback city.
type Skill struct {
	store      storage.Repository
	httpClient *http.Client
	registry   SkillLookup
	mu         sync.RWMutex
	owmKey     string // OpenWeatherMap API key (optional)
}

// New creates a new weather skill.
func New(store storage.Repository, registry SkillLookup, httpClient *http.Client) *Skill {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Skill{
		store:      store,
		httpClient: httpClient,
		registry:   registry,
	}
}

func (s *Skill) Name() string { return "weather" }

func (s *Skill) Description() string {
	return "Get current weather and forecasts for any location. " +
		"If no location is known, defaults to Иркутск (Irkutsk, Russia), without IP geolocation."
}

func (s *Skill) InputSchema() json.RawMessage {
	return json.RawMessage(`{
	"type": "object",
	"properties": {
		"location": {
			"type": "string",
			"description": "Known city or address, or exact lat, lon coordinates explicitly shared by the user. If no city is known, omit location: the tool defaults to Иркутск (Irkutsk, Russia). Never guess another city from language, server IP or earlier assistant guesses."
		},
		"days": {
			"type": "integer",
			"description": "Number of forecast days: 1 (today), 2 (today+tomorrow), up to 16. Default 1."
		}
	}
}`)
}

// OnConfigChanged implements skill.ConfigReloadable.
func (s *Skill) OnConfigChanged(key, value string) {
	if key != "skills.weather.openweathermap_api_key" {
		return
	}
	s.mu.Lock()
	s.owmKey = value
	s.mu.Unlock()
}

type weatherInput struct {
	Location string `json:"location"`
	Days     int    `json:"days"`
}

func (s *Skill) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in weatherInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	location := strings.TrimSpace(in.Location)
	days := in.Days
	if days < 1 {
		days = 1
	}
	if days > 16 {
		days = 16
	}
	if location == "" {
		resolved, err := resolveLocation(ctx, s.store, s.registry, interact.PrompterFrom(ctx))
		if err != nil {
			return "", err
		}
		location = defaults.CityOrDefault(resolved)
	}

	result, err := s.Forecast(ctx, location, days)
	if err != nil {
		return fmt.Sprintf("Unable to fetch weather for %q: %s. The service may be temporarily unavailable.", location, err), nil
	}

	return result, nil
}

// Forecast fetches an explicit location without consulting account memory,
// interactive prompts or server IP geolocation. Errors remain errors so callers
// can redact transport/provider details before returning them to a model.
func (s *Skill) Forecast(ctx context.Context, location string, days int) (string, error) {
	location = strings.TrimSpace(location)
	if location == "" {
		return "", fmt.Errorf("explicit weather location required")
	}
	if days < 1 {
		days = 1
	}
	if days > 16 {
		days = 16
	}
	result, err := s.buildBackendChain().Fetch(ctx, location, days)
	if err != nil {
		return "", err
	}
	return formatForecast(result, channel.CapsFrom(ctx)), nil
}

func (s *Skill) buildBackendChain() WeatherBackend {
	var backends []WeatherBackend

	s.mu.RLock()
	owmKey := s.owmKey
	s.mu.RUnlock()

	// Primary: Open-Meteo (free, no key).
	backends = append(backends, NewOpenMeteoBackend(s.httpClient))

	// Fallback: wttr.in (free, no key).
	backends = append(backends, NewWttrInBackend(s.httpClient))

	// Optional: OpenWeatherMap (requires key).
	if owmKey != "" {
		backends = append(backends, NewOWMBackend(s.httpClient, owmKey))
	}

	return newFallbackChain(backends...)
}

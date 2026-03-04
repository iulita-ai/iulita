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
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/skill/interact"
	"github.com/iulita-ai/iulita/internal/storage"
)

// SkillLookup finds a skill by name. Implemented by skill.Registry.
type SkillLookup interface {
	Get(name string) (skill.Skill, bool)
}

// Skill provides weather forecasts with interactive location resolution.
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
		"If no location is specified, asks the user or auto-detects via IP geolocation."
}

func (s *Skill) InputSchema() json.RawMessage {
	return json.RawMessage(`{
	"type": "object",
	"properties": {
		"location": {
			"type": "string",
			"description": "City name or address. Pass ONLY if the user explicitly mentioned a specific city in their message. If the user just says 'weather' or 'погода' without naming a city, leave this empty — the skill will ask the user interactively."
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

	// Resolve location if not provided.
	if location == "" {
		asker := interact.PrompterFrom(ctx)
		resolved, err := resolveLocation(ctx, s.store, s.registry, asker)
		if err != nil {
			return fmt.Sprintf("Unable to determine location: %s. Please specify a city name.", err), nil
		}
		location = resolved
	}

	if location == "" {
		return "Please specify a location for the weather forecast.", nil
	}

	// Build backend chain.
	chain := s.buildBackendChain()

	result, err := chain.Fetch(ctx, location, days)
	if err != nil {
		return fmt.Sprintf("Unable to fetch weather for %q: %s. The service may be temporarily unavailable.", location, err), nil
	}

	// Format based on channel capabilities.
	caps := channel.CapsFrom(ctx)
	return formatForecast(result, caps), nil
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

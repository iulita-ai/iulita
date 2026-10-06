package main

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/cost"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/eventbus"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/llm/claude"
	"github.com/iulita-ai/iulita/internal/llm/deepseek"
	"github.com/iulita-ai/iulita/internal/llm/ollama"
	openaillm "github.com/iulita-ai/iulita/internal/llm/openai"
	"github.com/iulita-ai/iulita/internal/llm/zai"
	"github.com/iulita-ai/iulita/internal/modelruntime"
	"github.com/iulita-ai/iulita/internal/models"
	"github.com/iulita-ai/iulita/internal/storage"
	"go.uber.org/zap"
)

func modelAuthorization(repo storage.Repository) modelruntime.Authorize {
	return func(ctx context.Context, actor string) error {
		user, err := repo.GetUser(ctx, actor)
		if err != nil || user == nil || user.Role != domain.RoleAdmin {
			return &modelruntime.Error{Code: "admin_required", Message: "Administrator access is required", HTTPStatus: 403}
		}
		if user.MustChangePass {
			return &modelruntime.Error{Code: "password_change_required", Message: "Change the initial password before configuring models", HTTPStatus: 403}
		}
		return nil
	}
}
func modelAttemptObserver(bus *eventbus.Bus, estimator *cost.Tracker) llm.AttemptObserver {
	return func(ctx context.Context, a llm.Attempt) {
		bus.Publish(ctx, eventbus.Event{Type: eventbus.LLMUsage, Payload: eventbus.UsagePayload(a, estimator.EstimateAttempt(a))})
	}
}
func modelClientFactory(client *http.Client, logger *zap.Logger, observe llm.AttemptObserver) modelruntime.Factory {
	return func(profile models.Profile, connection modelruntime.Connection) (llm.Provider, error) {
		var provider llm.Provider
		switch profile.Connection {
		case "deepseek", "zai":
			trusted, endpoint, err := llm.NewOfficialModelClient(profile.Connection, connection.Endpoint, client)
			if err != nil {
				return nil, err
			}
			d, known := models.Lookup(profile.Connection, profile.Model)
			options := deepseek.Options{Vision: known && d.Images, Thinking: profile.Thinking, ReasoningEffort: profile.ReasoningEffort}
			if profile.Connection == "deepseek" {
				provider = deepseek.NewWithOptions(connection.APIKey, profile.Model, profile.MaxOutputTokens, endpoint, trusted, logger, options)
			} else {
				provider = zai.NewWithOptions(connection.APIKey, profile.Model, profile.MaxOutputTokens, endpoint, trusted, logger, options)
			}
		case "claude":
			provider = claude.New(connection.APIKey, profile.Model, profile.MaxOutputTokens, connection.Endpoint, client)
		case "openai":
			provider = openaillm.New(connection.APIKey, profile.Model, profile.MaxOutputTokens, connection.Endpoint, client)
		case "ollama":
			provider = llm.NewXMLToolProvider(ollama.New(connection.Endpoint, profile.Model, client))
		default:
			return nil, &modelruntime.Error{Code: "invalid_connection", Message: "Unsupported model connection", HTTPStatus: 422}
		}
		return llm.NewObservingProvider(provider, llm.AttemptMetadata{Provider: profile.Connection, RequestedModel: profile.Model, ProfileID: profile.ID}, observe), nil
	}
}
func legacyModelConnections(cfg *config.Config) []modelruntime.Connection {
	var out []modelruntime.Connection
	for _, c := range []modelruntime.Connection{
		{Provider: "claude", Endpoint: cfg.Claude.BaseURL, APIKey: cfg.Claude.APIKey, Source: "legacy_effective", Enabled: true},
		{Provider: "openai", Endpoint: cfg.OpenAI.BaseURL, APIKey: cfg.OpenAI.APIKey, Source: "legacy_effective", Enabled: true},
		{Provider: "deepseek", Endpoint: cfg.DeepSeek.BaseURL, APIKey: cfg.DeepSeek.APIKey, Source: "legacy_effective", Enabled: true},
		{Provider: "zai", Endpoint: cfg.ZAI.BaseURL, APIKey: cfg.ZAI.APIKey, Source: "legacy_effective", Enabled: true},
	} {
		if c.APIKey != "" {
			if modelEnvironmentKey(c.Provider) != "" {
				c.Source = "environment"
			}
			out = append(out, c)
		}
	}
	if cfg.Ollama.URL != "" {
		out = append(out, modelruntime.Connection{Provider: "ollama", Endpoint: cfg.Ollama.URL, Source: "legacy_effective", Enabled: true})
	}
	return out
}

// changedLegacyModelProviders excludes normalized protocols from exact legacy grants.
func changedLegacyModelProviders(cfg *config.Config, thinking string) []string {
	var changed []string
	if thinking == "low" || thinking == "medium" || thinking == "high" || cfg.Claude.MaxTokens > models.MaxDeploymentOutputTokens {
		changed = append(changed, "claude")
	}
	if cfg.OpenAI.MaxTokens > models.MaxDeploymentOutputTokens {
		changed = append(changed, "openai")
	}
	return changed
}

// legacyModelSettings is a migration draft, never an activation or paid check.
// Every custom hint keeps its original distinct provider target.
func legacyModelSettings(cfg *config.Config) *models.Settings {
	s := models.Settings{SchemaVersion: 1, Policy: models.Policy{LegacyProfileHints: map[string]string{}, LegacyHints: map[string]string{}}}
	targets := map[string]string{}
	add := func(provider, model string, limit int) {
		if model == "" {
			return
		}
		if limit <= 0 {
			limit = 4096
		}
		if limit > models.MaxDeploymentOutputTokens {
			limit = models.MaxDeploymentOutputTokens
		}
		id := "legacy-" + provider
		connection := provider
		if provider == "claude-haiku" {
			connection = "claude"
		}
		s.Profiles = append(s.Profiles, models.Profile{ID: id, Name: provider + " / " + model, Connection: connection, Model: model, MaxOutputTokens: limit, Thinking: "disabled"})
		targets[provider] = id
	}
	if cfg.Claude.APIKey != "" {
		add("claude", cfg.Claude.Model, cfg.Claude.MaxTokens)
		if cfg.Claude.Model != "claude-haiku-4-5" && cfg.Claude.Model != "claude-haiku-4-5-20251001" {
			add("claude-haiku", "claude-haiku-4-5-20251001", cfg.Claude.MaxTokens)
		}
	}
	if cfg.OpenAI.APIKey != "" {
		add("openai", cfg.OpenAI.Model, cfg.OpenAI.MaxTokens)
	}
	if cfg.DeepSeek.APIKey != "" {
		model := cfg.DeepSeek.Model
		if model == "deepseek-v4-flash" || model == "deepseek-v4-flash-vision-exp" {
			model = "deepseek-flash"
		}
		if _, ok := models.Lookup("deepseek", model); !ok {
			return nil
		}
		add("deepseek", model, cfg.DeepSeek.MaxTokens)
	}
	if cfg.Ollama.URL != "" {
		add("ollama", cfg.Ollama.Model, 4096)
	}
	primary := targets[cfg.Routing.DefaultProvider]
	if primary == "" {
		for _, provider := range []string{"claude", "openai", "deepseek", "ollama"} {
			if primary = targets[provider]; primary != "" {
				break
			}
		}
	}
	if primary == "" {
		return nil
	}
	s.Policy.Everyday = primary
	s.Policy.Complex = primary
	s.Policy.Background = primary
	if cfg.Routing.LightEnabled {
		if id := targets[cfg.Routing.LightProvider]; id != "" {
			s.Policy.Background = id
		}
	}
	for _, provider := range []string{"claude", "claude-haiku"} {
		if id := targets[provider]; id != "" {
			s.Policy.Vision = id
			break
		}
	}
	for hint, provider := range map[string]string{"openai": "openai", "deepseek": "deepseek", "ollama": "ollama"} {
		if id := targets[provider]; id != "" {
			s.Policy.LegacyProfileHints[hint] = id
		}
	}
	for _, route := range cfg.Routing.Routes {
		if id := targets[route.Provider]; id != "" {
			s.Policy.LegacyProfileHints[route.Hint] = id
			if route.Hint == "complex" {
				s.Policy.Complex = id
			}
		}
	}
	s.Policy.LegacyHints[llm.RouteHintCheap] = "background"
	delete(s.Policy.LegacyProfileHints, llm.RouteHintCheap)
	s.Policy.LegacyHints[llm.RouteHintVision] = "vision"
	delete(s.Policy.LegacyProfileHints, llm.RouteHintVision)
	return &s
}

func modelEnvironmentKey(provider string) string {
	if key := os.Getenv("IULITA_" + strings.ToUpper(provider) + "_API_KEY"); key != "" {
		return key
	}
	env := map[string]string{"claude": "ANTHROPIC_API_KEY", "openai": "OPENAI_API_KEY", "deepseek": "DEEPSEEK_API_KEY", "zai": "ZAI_API_KEY"}[provider]
	if env != "" {
		return os.Getenv(env)
	}
	return ""
}
func applyModelEnvironmentKeys(cfg *config.Config) {
	for provider, target := range map[string]*string{"claude": &cfg.Claude.APIKey, "openai": &cfg.OpenAI.APIKey, "deepseek": &cfg.DeepSeek.APIKey, "zai": &cfg.ZAI.APIKey} {
		if key := modelEnvironmentKey(provider); key != "" {
			*target = key
		}
	}
}
func closeModelRuntime(manager *modelruntime.Manager, logger *zap.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := manager.Close(ctx); err != nil {
		logger.Error("model interruption state could not be saved", zap.Error(err))
	}
	if err := manager.Wait(ctx); err != nil {
		logger.Warn("model requests did not drain before shutdown deadline", zap.Error(err))
	}
}

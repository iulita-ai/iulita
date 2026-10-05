// Package zai implements the Z.ai standard metered API. It shares the bounded
// OpenAI-shaped protocol with DeepSeek while fixing GLM-specific thinking and
// provider identity; subscription Coding Plan endpoints are never inferred.
package zai

import (
	"net/http"
	"strings"

	"github.com/iulita-ai/iulita/internal/llm/deepseek"
	"go.uber.org/zap"
)

const defaultBaseURL = "https://api.z.ai/api/paas/v4"

type Provider = deepseek.Provider
type Options = deepseek.Options

// New selects the documented capability for a GLM 5.3 model and low reasoning
// effort. The supplied HTTP client must enforce the endpoint trust boundary.
func New(apiKey, model string, maxTokens int, baseURL string, httpClient *http.Client, logger *zap.Logger) *Provider {
	return NewWithOptions(apiKey, model, maxTokens, baseURL, httpClient, logger, Options{Vision: strings.EqualFold(model, "glm-5.3-flash"), ReasoningEffort: "low"})
}

// NewWithOptions fixes GLM's required thinking mode and clear_thinking=true
// protocol. A requested disabled mode is retained so the adapter rejects it
// locally rather than silently changing the user's configuration.
func NewWithOptions(apiKey, model string, maxTokens int, baseURL string, httpClient *http.Client, logger *zap.Logger, options Options) *Provider {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	options.ProviderName = "zai"
	if options.Thinking == "" {
		options.Thinking = "enabled"
	}
	if options.ReasoningEffort == "" {
		options.ReasoningEffort = "low"
	}
	return deepseek.NewWithOptions(apiKey, model, maxTokens, baseURL, httpClient, logger, options)
}

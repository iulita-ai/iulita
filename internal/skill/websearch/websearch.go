package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/iulita-ai/iulita/internal/web"
)

// Skill performs web searches via configurable search providers.
type Skill struct {
	client   web.Searcher     // active searcher (possibly FallbackSearcher)
	brave    *web.BraveClient // retained for hot-reload API key updates
	capAdder capabilityAdder
	cfgStore configReader
}

type capabilityAdder interface {
	AddCapability(cap string)
	RemoveCapability(cap string)
}

type configReader interface {
	GetEffective(key string) (string, bool)
}

// New creates a websearch skill with the given searcher and optional Brave client for hot-reload.
func New(client web.Searcher, brave *web.BraveClient) *Skill {
	return &Skill{client: client, brave: brave}
}

// SetReloader enables hot-reload for config changes.
func (s *Skill) SetReloader(capAdder capabilityAdder, cfgStore configReader) {
	s.capAdder = capAdder
	s.cfgStore = cfgStore
}

// OnConfigChanged reacts to runtime config updates.
func (s *Skill) OnConfigChanged(key, value string) {
	if s.cfgStore == nil {
		return
	}

	apiKey, _ := s.cfgStore.GetEffective("skills.web.api_key")
	if apiKey != "" && s.brave != nil {
		s.brave.UpdateAPIKey(apiKey)
	}
	// Capability stays on regardless — DDG fallback always works.
	if s.capAdder != nil {
		s.capAdder.AddCapability("web")
	}
}

func (s *Skill) Name() string { return "websearch" }

func (s *Skill) Description() string {
	return "Search the web for current information. Returns titles, URLs, and descriptions of matching pages."
}

func (s *Skill) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {
				"type": "string",
				"description": "Search query"
			},
			"count": {
				"type": "integer",
				"description": "Number of results (1-10, default 5)"
			}
		},
		"required": ["query"]
	}`)
}

func (s *Skill) RequiredCapabilities() []string {
	return []string{"web"}
}

type input struct {
	Query string `json:"query"`
	Count int    `json:"count"`
}

func (s *Skill) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in input
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	if in.Query == "" {
		return "", fmt.Errorf("query is required")
	}

	count := in.Count
	if count <= 0 {
		count = 5
	}
	if count > 10 {
		count = 10
	}

	results, err := s.client.Search(ctx, in.Query, count)
	if err != nil {
		return "", fmt.Errorf("search failed: %w", err)
	}

	if len(results) == 0 {
		return "No results found.", nil
	}

	var b strings.Builder
	for i, r := range results {
		fmt.Fprintf(&b, "%d. **%s**\n   %s\n   %s\n\n", i+1, r.Title, r.URL, r.Description)
	}
	return b.String(), nil
}

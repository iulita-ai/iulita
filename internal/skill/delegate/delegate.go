package delegate

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"

	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/skill"
)

//go:embed SKILL.md
var skillFS embed.FS

// LoadManifest reads the embedded SKILL.md and returns the skill manifest.
func LoadManifest() (*skill.Manifest, error) {
	return skill.LoadManifestFromFS(skillFS, "SKILL.md")
}

type delegateInput struct {
	ProfileID string `json:"profile_id"`
	Prompt    string `json:"prompt"`
	Provider  string `json:"provider"` // optional: "ollama", "openai" — defaults to secondary
}

// Skill delegates a subtask to a secondary LLM provider.
type Skill struct {
	providers       map[string]llm.Provider
	defaultProvider string
	router          llm.Provider
}

// New creates a new delegate skill with named providers.
func New(providers map[string]llm.Provider, defaultProvider string) *Skill {
	if providers == nil {
		providers = make(map[string]llm.Provider)
	}
	return &Skill{
		providers:       providers,
		defaultProvider: defaultProvider,
	}
}

// NewRouted delegates through the common profile router. The explicit
// profile_id is never inferred from the legacy provider hint.
func NewRouted(router llm.Provider) *Skill { return &Skill{router: router} }

// OnConfigChanged implements skill.ConfigReloadable.
func (s *Skill) OnConfigChanged(key, value string) {
	if key == "skills.delegate.default_provider" && value != "" {
		s.defaultProvider = value
	}
}

func (s *Skill) Name() string { return "delegate" }

func (s *Skill) Description() string {
	return "Delegate a subtask to a secondary LLM provider for translations, summaries, or simple lookups"
}

func (s *Skill) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"prompt": {"type": "string", "description": "The prompt to send to the secondary LLM"},
			"profile_id": {"type":"string","description":"Optional configured model profile ID."},
			"provider": {"type": "string", "description": "Optional provider name (e.g. 'ollama', 'openai'). Uses default if omitted."}
		},
		"required": ["prompt"]
	}`)
}

func (s *Skill) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var inp delegateInput
	if err := json.Unmarshal(input, &inp); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	if inp.Prompt == "" {
		return "", fmt.Errorf("prompt is required")
	}

	if s.router != nil {
		role := ""
		if inp.Provider == "" || inp.ProfileID != "" {
			role = "background"
		}
		resp, err := s.router.Complete(ctx, llm.Request{Message: inp.Prompt, ProfileID: inp.ProfileID, RouteHint: inp.Provider, Role: role, Operation: "delegate", ChatID: skill.ChatIDFrom(ctx), UserID: skill.UserIDFrom(ctx)})
		if err != nil {
			return "", fmt.Errorf("delegate: %w", err)
		}
		return resp.Content, nil
	}
	// Determine which provider to use.
	providerName := s.defaultProvider
	if inp.Provider != "" {
		providerName = inp.Provider
	}

	provider, ok := s.providers[providerName]
	if !ok {
		return "", fmt.Errorf("unknown provider %q; available: %s", providerName, s.availableProviders())
	}

	resp, err := provider.Complete(ctx, llm.Request{
		Message: inp.Prompt,
	})
	if err != nil {
		return "", fmt.Errorf("delegate to %s: %w", providerName, err)
	}

	return resp.Content, nil
}

func (s *Skill) availableProviders() string {
	names := make([]string, 0, len(s.providers))
	for name := range s.providers {
		names = append(names, name)
	}
	if len(names) == 0 {
		return "(none)"
	}
	result := names[0]
	for _, n := range names[1:] {
		result += ", " + n
	}
	return result
}

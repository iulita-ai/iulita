package orchestrate

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/agent"
	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/eventbus"
	"github.com/iulita-ai/iulita/internal/llm"
	skpkg "github.com/iulita-ai/iulita/internal/skill"
)

//go:embed SKILL.md
var skillFS embed.FS

// LoadManifest reads the embedded SKILL.md and returns the skill manifest.
func LoadManifest() (*skpkg.Manifest, error) {
	return skpkg.LoadManifestFromFS(skillFS, "SKILL.md")
}

type orchestrateInput struct {
	Agents    []agentSpecInput `json:"agents"`
	Timeout   string           `json:"timeout,omitempty"`    // Go duration string
	MaxTokens int64            `json:"max_tokens,omitempty"` // shared budget
}

type agentSpecInput struct {
	ID        string   `json:"id"`
	Type      string   `json:"type"`
	Task      string   `json:"task"`
	RouteHint string   `json:"route_hint,omitempty"`
	Tools     []string `json:"tools,omitempty"`
}

// Skill implements the orchestrate tool for multi-agent parallel execution.
type Skill struct {
	provider llm.Provider
	registry *skpkg.Registry
	notifier channel.StatusNotifier
	bus      *eventbus.Bus
	logger   *zap.Logger

	mu             sync.RWMutex
	maxTokens      int64
	maxAgents      int
	timeout        time.Duration // per-agent timeout
	requestTimeout time.Duration // overall orchestration timeout (for TimeoutDeclarer)
}

// New creates a new orchestrate skill.
func New(provider llm.Provider, registry *skpkg.Registry, bus *eventbus.Bus, logger *zap.Logger) *Skill {
	return &Skill{
		provider:  provider,
		registry:  registry,
		bus:       bus,
		logger:    logger,
		maxTokens: 0, // unlimited by default
		maxAgents: 5, // default max parallel agents
		timeout:   60 * time.Second,
	}
}

// SetNotifier sets the status notifier for sending agent progress events.
// Called after the channel manager and assistant are fully wired.
func (s *Skill) SetNotifier(n channel.StatusNotifier) {
	s.notifier = n
}

// SetEventBus sets the event bus for publishing orchestration events.
// Called after the event bus is created.
func (s *Skill) SetEventBus(b *eventbus.Bus) {
	s.bus = b
}

// maxRequestTimeout is the hard cap for orchestration request timeout.
const maxRequestTimeout = 4 * time.Hour

// RequestTimeout implements skill.TimeoutDeclarer.
// Returns the configured orchestration-level timeout (default 1h, max 4h).
// This allows the assistant to extend its context deadline when orchestrate is invoked.
func (s *Skill) RequestTimeout() time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d := s.requestTimeout
	if d <= 0 {
		d = time.Hour // default
	}
	if d > maxRequestTimeout {
		d = maxRequestTimeout
	}
	return d
}

// OnConfigChanged implements skill.ConfigReloadable.
func (s *Skill) OnConfigChanged(key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch key {
	case "skills.orchestrate.max_tokens":
		if v, err := parseInt64(value); err == nil {
			s.maxTokens = v
		}
	case "skills.orchestrate.max_agents":
		if v, err := parseInt64(value); err == nil && v > 0 {
			s.maxAgents = int(v)
		}
	case "skills.orchestrate.timeout":
		if d, err := time.ParseDuration(value); err == nil && d > 0 {
			s.timeout = d
		}
	case "skills.orchestrate.request_timeout":
		if value == "" {
			s.requestTimeout = 0 // resets to default in RequestTimeout()
		} else if d, err := time.ParseDuration(value); err == nil && d > 0 {
			s.requestTimeout = d
		}
	}
}

func (s *Skill) Name() string { return "orchestrate" }

func (s *Skill) Description() string {
	return "Launch multiple specialized sub-agents in parallel to work on different aspects of a complex task"
}

func (s *Skill) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"agents": {
				"type": "array",
				"description": "List of sub-agents to launch in parallel",
				"items": {
					"type": "object",
					"properties": {
						"id":         {"type": "string", "description": "Unique identifier for this agent"},
						"type":       {"type": "string", "enum": ["researcher","analyst","planner","coder","summarizer","generic"], "description": "Agent type determining its system prompt and available tools"},
						"task":       {"type": "string", "description": "The specific task for this agent to complete"},
						"route_hint": {"type": "string", "description": "Optional LLM provider routing hint (e.g. 'ollama')"},
						"tools":      {"type": "array", "items": {"type": "string"}, "description": "Optional explicit tool allowlist for this agent"}
					},
					"required": ["id", "type", "task"]
				},
				"maxItems": 5
			},
			"timeout":    {"type": "string", "description": "Per-agent timeout as Go duration (e.g. '60s', '2m'). Default: 60s"},
			"max_tokens": {"type": "integer", "description": "Shared token budget across all agents. Default: unlimited"}
		},
		"required": ["agents"]
	}`)
}

// Execute runs the multi-agent orchestration.
func (s *Skill) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var inp orchestrateInput
	if err := json.Unmarshal(input, &inp); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	if len(inp.Agents) == 0 {
		return "error: at least one agent is required", nil
	}

	// Depth check — sub-agents cannot spawn further sub-agents.
	if agent.DepthFrom(ctx) >= agent.MaxDepth {
		return "error: sub-agents cannot spawn further sub-agents (max depth exceeded)", nil
	}

	// Build agent specs.
	specs := make([]agent.AgentSpec, len(inp.Agents))
	for i, a := range inp.Agents {
		specs[i] = agent.AgentSpec{
			ID:        a.ID,
			Type:      agent.ParseAgentType(a.Type),
			Task:      a.Task,
			RouteHint: a.RouteHint,
			Tools:     a.Tools,
		}
		// Auto-assign ID if empty.
		if specs[i].ID == "" {
			specs[i].ID = fmt.Sprintf("agent_%d", i+1)
		}
	}

	// Build budget (read config under lock to avoid race with OnConfigChanged).
	s.mu.RLock()
	budget := agent.Budget{
		MaxTokens: s.maxTokens,
		MaxAgents: s.maxAgents,
		Timeout:   s.timeout,
	}
	s.mu.RUnlock()
	if inp.MaxTokens > 0 {
		budget.MaxTokens = inp.MaxTokens
	}
	if inp.Timeout != "" {
		if d, err := time.ParseDuration(inp.Timeout); err == nil && d > 0 {
			budget.Timeout = d
		}
	}

	chatID := skpkg.ChatIDFrom(ctx)

	// Create and run the orchestrator.
	orch := agent.NewOrchestrator(s.provider, s.registry, s.notifier, s.bus, s.logger)
	results, err := orch.Run(ctx, chatID, specs, budget)
	if err != nil {
		return fmt.Sprintf("orchestration failed: %v", err), nil
	}

	// Format results as structured markdown.
	return formatResults(results), nil
}

// formatResults builds a markdown summary of all agent results.
func formatResults(results []agent.AgentResult) string {
	var b strings.Builder
	for _, r := range results {
		fmt.Fprintf(&b, "## Agent: %s (%s)\n", r.ID, r.Type)
		if r.Err != nil {
			fmt.Fprintf(&b, "> Error: %v\n\n", r.Err)
			if r.Output != "" {
				fmt.Fprintf(&b, "Partial output:\n%s\n\n", r.Output)
			}
		} else {
			b.WriteString(r.Output)
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "_Turns: %d | Tokens: %d | Duration: %s_\n\n", r.Turns, r.Tokens, r.Duration.Round(time.Millisecond))
	}
	return b.String()
}

func parseInt64(s string) (int64, error) {
	var v int64
	_, err := fmt.Sscanf(s, "%d", &v)
	return v, err
}

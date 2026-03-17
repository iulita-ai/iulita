package agent

import (
	"errors"
	"time"
)

// AgentType identifies a predefined sub-agent persona.
type AgentType string //nolint:revive // agent.AgentType is more readable than agent.Type in external usage

// AgentType constants define the available sub-agent personas.
const (
	AgentTypeResearcher AgentType = "researcher"
	AgentTypeAnalyst    AgentType = "analyst"
	AgentTypePlanner    AgentType = "planner"
	AgentTypeCoder      AgentType = "coder"
	AgentTypeSummarizer AgentType = "summarizer"
	AgentTypeGeneric    AgentType = "generic"
)

// ErrBudgetExhausted is returned when the shared token budget is depleted.
var ErrBudgetExhausted = errors.New("shared token budget exhausted")

// Status event type constants emitted by sub-agents and orchestrator.
const (
	EventOrchestrationStarted = "orchestration_started"
	EventOrchestrationDone    = "orchestration_done"
	EventAgentStarted         = "agent_started"
	EventAgentProgress        = "agent_progress"
	EventAgentCompleted       = "agent_completed"
	EventAgentFailed          = "agent_failed"
)

// Budget defines resource constraints for a sub-agent run or an entire orchestration.
type Budget struct {
	MaxTokens int64         // shared across all agents; 0 = unlimited
	MaxTurns  int           // per-agent LLM call limit; 0 defaults to DefaultMaxTurns
	Timeout   time.Duration // per-agent wall-clock timeout; 0 defaults to DefaultTimeout
	MaxAgents int           // max parallel agents; 0 defaults to DefaultMaxAgents
}

// DefaultMaxTurns is the default per-agent iteration limit.
const DefaultMaxTurns = 10

// DefaultTimeout is the default per-agent wall-clock timeout.
const DefaultTimeout = 60 * time.Second

// DefaultMaxAgents is the default maximum number of parallel agents.
const DefaultMaxAgents = 5

// EffectiveMaxTurns returns MaxTurns or the default if zero.
func (b Budget) EffectiveMaxTurns() int {
	if b.MaxTurns > 0 {
		return b.MaxTurns
	}
	return DefaultMaxTurns
}

// EffectiveTimeout returns Timeout or the default if zero.
func (b Budget) EffectiveTimeout() time.Duration {
	if b.Timeout > 0 {
		return b.Timeout
	}
	return DefaultTimeout
}

// EffectiveMaxAgents returns MaxAgents or the default if zero.
func (b Budget) EffectiveMaxAgents() int {
	if b.MaxAgents > 0 {
		return b.MaxAgents
	}
	return DefaultMaxAgents
}

// AgentSpec is a single agent task as provided by the orchestrate skill input.
type AgentSpec struct { //nolint:revive // agent.AgentSpec is more readable than agent.Spec
	ID        string    `json:"id"`
	Type      AgentType `json:"type"`
	Task      string    `json:"task"`
	RouteHint string    `json:"route_hint,omitempty"` // optional: routing hint for provider selection
	Tools     []string  `json:"tools,omitempty"`      // optional: explicit tool name allowlist
}

// AgentResult holds the outcome of a single sub-agent run.
type AgentResult struct { //nolint:revive // agent.AgentResult is more readable than agent.Result
	ID       string
	Type     AgentType
	Output   string
	Turns    int
	Tokens   int64
	Duration time.Duration
	Err      error
}

// AgentTypeProfile holds the per-type configuration.
type AgentTypeProfile struct { //nolint:revive // agent.AgentTypeProfile is more readable than agent.TypeProfile
	SystemPrompt string
	RouteHint    string   // default provider hint
	DefaultTools []string // tool names; nil = all registered tools
}

// ParseAgentType converts a string to an AgentType, defaulting to AgentTypeGeneric.
func ParseAgentType(s string) AgentType {
	switch AgentType(s) {
	case AgentTypeResearcher, AgentTypeAnalyst, AgentTypePlanner,
		AgentTypeCoder, AgentTypeSummarizer, AgentTypeGeneric:
		return AgentType(s)
	default:
		return AgentTypeGeneric
	}
}

// Profiles returns the built-in profiles for all agent types.
func Profiles() map[AgentType]AgentTypeProfile {
	return map[AgentType]AgentTypeProfile{
		AgentTypeResearcher: {
			SystemPrompt: "You are a research sub-agent. Your job is to gather information, search the web, and produce a structured summary of findings. Do not make recommendations — only present what you found, clearly organized.",
			DefaultTools: []string{"web_search", "webfetch"},
		},
		AgentTypeAnalyst: {
			SystemPrompt: "You are an analysis sub-agent. Given data or a research brief, identify patterns, anomalies, and key insights. Be concise and well-structured. Use bullet points for clarity.",
		},
		AgentTypePlanner: {
			SystemPrompt: "You are a planning sub-agent. Decompose complex goals into ordered steps with clear dependencies. Output a markdown task list. Focus on actionable items.",
			DefaultTools: []string{"datetime"},
		},
		AgentTypeCoder: {
			SystemPrompt: "You are a coding sub-agent. Write, review, or debug code as requested. Prefer complete, runnable examples. Be precise and explain key decisions briefly.",
		},
		AgentTypeSummarizer: {
			SystemPrompt: "You are a summarization sub-agent. Condense the provided input to its essential points. Use bullet points. Maximum 300 words unless instructed otherwise.",
			RouteHint:    "ollama",
		},
		AgentTypeGeneric: {
			SystemPrompt: "You are a general-purpose sub-agent. Complete the given task efficiently and concisely.",
		},
	}
}

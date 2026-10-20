package skill

import (
	"context"
	"encoding/json"
)

// Skill defines a capability that can be invoked by the LLM via tool_use.
type Skill interface {
	Name() string
	Description() string
	InputSchema() json.RawMessage
	Execute(ctx context.Context, input json.RawMessage) (string, error)
}

// CapabilityAware is an optional interface that skills can implement to declare
// required capabilities. Skills without this interface are always available.
type CapabilityAware interface {
	RequiredCapabilities() []string
}

// ConfigReloadable is an optional interface for skills that can react to
// runtime config changes. Called when a config key declared in the skill's
// manifest ConfigKeys is updated via the API.
type ConfigReloadable interface {
	OnConfigChanged(key, value string)
}

// SynthesisModelDeclarer is an optional interface for skills that want the
// post-execution synthesis LLM call to use a specific provider route.
// The returned string must match a registered RouteHint in the LLM routing
// provider (e.g., llm.RouteHintCheap). Falls back silently if not registered.
// When multiple tools execute in one iteration, last non-empty hint wins.
// The hint only applies to the next iteration's LLM call, then resets.
type SynthesisModelDeclarer interface {
	SynthesisRouteHint() string
}

// Result is a trusted native skill outcome. Delivery metadata never comes from
// tool arguments or provider text and is not serialized into an LLM request.
type Result struct {
	Text     string
	Verbatim bool
}

// ResultExecutor optionally returns a final native result without synthesis.
// Existing skills continue to use Execute. The dispatcher invokes only one.
type ResultExecutor interface {
	ExecuteResult(context.Context, json.RawMessage) (Result, error)
}

// TerminalDeclarer marks a native operation whose success and errors finish the
// turn. Such operations cannot be combined with other tools in one batch.
type TerminalDeclarer interface{ TerminalResult() bool }

// MainLoopOnly prevents hidden paid operations bypassing subagent budgets.
type MainLoopOnly interface{ MainLoopOnly() bool }

// AvailabilityAware checks current service admission metadata without secrets.
type AvailabilityAware interface{ Available() bool }

// ServiceStatus describes a specialized service without exposing credentials.
type ServiceStatus struct {
	Provider string `json:"provider"`
	Service  string `json:"service"`
	State    string `json:"state"`
	Checked  bool   `json:"checked"`
}

// ServiceStatusDeclarer exposes only nonsecret specialized-service metadata.
type ServiceStatusDeclarer interface{ ServiceStatus() ServiceStatus }

// DefaultEnabledDeclarer lets opt-in skills persist a real enabled override.
type DefaultEnabledDeclarer interface{ DefaultEnabled() bool }

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

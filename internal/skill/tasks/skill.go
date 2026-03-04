package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/iulita-ai/iulita/internal/skill"
	"go.uber.org/zap"
)

// Provider is a task backend that the meta-skill can route to.
// Any skill.Skill implementation satisfies this interface.
type Provider interface {
	// Name returns the skill name (e.g. "todoist", "google_tasks", "craft_tasks").
	Name() string
	// Execute runs the action with raw JSON input and returns the result.
	Execute(ctx context.Context, input json.RawMessage) (string, error)
}

// Skill is a meta-skill that unifies task management across multiple providers
// (Todoist, Google Tasks, Craft Tasks). It routes requests to available providers
// based on capabilities.
type Skill struct {
	registry      capabilityChecker
	providers     map[string]providerEntry
	providerOrder []string // deterministic ordering
	logger        *zap.Logger
}

type providerEntry struct {
	provider    Provider
	capability  string // required capability for this provider
	displayName string
}

type capabilityChecker interface {
	// Get returns a skill by name if enabled and has required capabilities.
	Get(name string) (skill.Skill, bool)
}

// NewSkill creates the unified tasks meta-skill.
func NewSkill(registry capabilityChecker, logger *zap.Logger) *Skill {
	return &Skill{
		registry:  registry,
		providers: make(map[string]providerEntry),
		logger:    logger,
	}
}

// RegisterProvider adds a task provider. Providers are iterated in registration order.
func (s *Skill) RegisterProvider(name, capability string, provider Provider) {
	if _, exists := s.providers[name]; !exists {
		s.providerOrder = append(s.providerOrder, name)
	}
	s.providers[name] = providerEntry{
		provider:    provider,
		capability:  capability,
		displayName: name,
	}
}

func (s *Skill) Name() string { return "tasks" }

func (s *Skill) Description() string {
	return "Unified task management across all connected task services (Todoist, Google Tasks, Craft). List tasks from all providers, create/complete/manage tasks in any connected service. Routes to the appropriate provider automatically."
}

func (s *Skill) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["overview", "list", "create", "complete", "provider"],
				"description": "Action: overview (tasks across all providers), list (tasks from one provider), create (in specific provider), complete (in specific provider), provider (run any action on a specific provider by forwarding raw input)"
			},
			"provider": {
				"type": "string",
				"description": "Target provider: 'todoist', 'google_tasks', or 'craft_tasks'. Required for list, create, complete, provider actions. For overview, omit to query all providers."
			},
			"filter": {
				"type": "string",
				"description": "For overview/list: filter to apply. For todoist: filter query (e.g. 'today | overdue'). For google_tasks: ignored. For overview without filter: shows today + overdue from all sources."
			},
			"content": {
				"type": "string",
				"description": "Task title/content (for create action)"
			},
			"task_id": {
				"type": "string",
				"description": "Task ID (for complete action)"
			},
			"due_string": {
				"type": "string",
				"description": "Due date as natural language (for create): 'tomorrow', 'next Monday', 'every Friday'"
			},
			"due_date": {
				"type": "string",
				"description": "Due date in YYYY-MM-DD format (for create, alternative to due_string)"
			},
			"priority": {
				"type": "string",
				"description": "Priority for create (Todoist only): P1, P2, P3, P4"
			},
			"provider_input": {
				"type": "object",
				"description": "Raw input to forward to the provider skill (for 'provider' action). Use this to access provider-specific features not covered by the unified interface."
			}
		},
		"required": ["action"]
	}`)
}

func (s *Skill) RequiredCapabilities() []string {
	// The meta-skill itself doesn't require a fixed capability.
	// It's always registered; individual providers are checked at runtime.
	return nil
}

type tasksInput struct {
	Action        string          `json:"action"`
	Provider      string          `json:"provider"`
	Filter        string          `json:"filter"`
	Content       string          `json:"content"`
	TaskID        string          `json:"task_id"`
	DueString     string          `json:"due_string"`
	DueDate       string          `json:"due_date"`
	Priority      string          `json:"priority"`
	ProviderInput json.RawMessage `json:"provider_input"`
}

func (s *Skill) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in tasksInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	available := s.availableProviders()
	if len(available) == 0 {
		s.logger.Debug("tasks meta-skill: no providers available")
		return "No task providers are connected. Configure at least one of: Todoist (API token), Google Tasks (OAuth2), or Craft (API key) in Settings.", nil
	}

	var provNames []string
	for _, pe := range available {
		provNames = append(provNames, pe.displayName)
	}
	s.logger.Debug("tasks meta-skill dispatch",
		zap.String("action", in.Action),
		zap.String("requested_provider", in.Provider),
		zap.Strings("available_providers", provNames),
	)

	switch in.Action {
	case "overview":
		return s.overview(ctx, in, available)
	case "list":
		return s.list(ctx, in, available)
	case "create":
		return s.create(ctx, in, available)
	case "complete":
		return s.complete(ctx, in, available)
	case "provider":
		return s.providerAction(ctx, in, available)
	default:
		return "", fmt.Errorf("unknown action %q (use: overview, list, create, complete, provider)", in.Action)
	}
}

// availableProviders returns providers whose capabilities are met, in registration order.
func (s *Skill) availableProviders() []providerEntry {
	var result []providerEntry
	for _, name := range s.providerOrder {
		pe := s.providers[name]
		// Check if the underlying skill is available (enabled + has capabilities).
		if _, ok := s.registry.Get(pe.provider.Name()); ok {
			result = append(result, pe)
		}
	}
	return result
}

func (s *Skill) findProvider(name string, available []providerEntry) (*providerEntry, error) {
	for _, pe := range available {
		if pe.displayName == name {
			return &pe, nil
		}
	}

	// List available providers in error.
	var names []string
	for _, pe := range available {
		names = append(names, pe.displayName)
	}
	return nil, fmt.Errorf("provider %q not available. Connected providers: %s", name, strings.Join(names, ", "))
}

func (s *Skill) overview(ctx context.Context, in tasksInput, available []providerEntry) (string, error) {
	var b strings.Builder
	b.WriteString("## Task Overview\n\n")

	var names []string
	for _, pe := range available {
		names = append(names, pe.displayName)
	}
	fmt.Fprintf(&b, "Connected providers: %s\n\n", strings.Join(names, ", "))

	for _, pe := range available {
		input := s.buildListInput(pe.displayName, in.Filter)
		result, err := pe.provider.Execute(ctx, input)
		if err != nil {
			fmt.Fprintf(&b, "### %s\n\nError: %v\n\n", pe.displayName, err)
			continue
		}
		fmt.Fprintf(&b, "### %s\n\n%s\n\n", pe.displayName, result)
	}

	return b.String(), nil
}

func (s *Skill) list(ctx context.Context, in tasksInput, available []providerEntry) (string, error) {
	if in.Provider == "" {
		// If no provider specified, behave like overview.
		return s.overview(ctx, in, available)
	}

	pe, err := s.findProvider(in.Provider, available)
	if err != nil {
		return "", err
	}

	input := s.buildListInput(pe.displayName, in.Filter)
	return pe.provider.Execute(ctx, input)
}

func (s *Skill) create(ctx context.Context, in tasksInput, available []providerEntry) (string, error) {
	if in.Content == "" {
		return "", fmt.Errorf("content is required for create action")
	}

	provider := in.Provider
	if provider == "" {
		// Default to first available provider.
		provider = available[0].displayName
		s.logger.Info("tasks create: no provider specified, defaulting",
			zap.String("default_provider", provider),
		)
	}

	pe, err := s.findProvider(provider, available)
	if err != nil {
		return "", err
	}

	input := s.buildCreateInput(pe.displayName, in)
	result, err := pe.provider.Execute(ctx, input)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("[%s] %s", pe.displayName, result), nil
}

func (s *Skill) complete(ctx context.Context, in tasksInput, available []providerEntry) (string, error) {
	if in.TaskID == "" {
		return "", fmt.Errorf("task_id is required for complete action")
	}
	if in.Provider == "" {
		return "", fmt.Errorf("provider is required for complete action (specify which service the task belongs to)")
	}

	pe, err := s.findProvider(in.Provider, available)
	if err != nil {
		return "", err
	}

	input := s.buildCompleteInput(pe.displayName, in.TaskID)
	result, err := pe.provider.Execute(ctx, input)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("[%s] %s", pe.displayName, result), nil
}

func (s *Skill) providerAction(ctx context.Context, in tasksInput, available []providerEntry) (string, error) {
	if in.Provider == "" {
		return "", fmt.Errorf("provider is required for provider action")
	}

	pe, err := s.findProvider(in.Provider, available)
	if err != nil {
		return "", err
	}

	if in.ProviderInput == nil {
		return "", fmt.Errorf("provider_input is required for provider action")
	}

	result, err := pe.provider.Execute(ctx, in.ProviderInput)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("[%s] %s", pe.displayName, result), nil
}

// --- Input builders for different providers ---

func (s *Skill) buildListInput(provider, filter string) json.RawMessage {
	switch provider {
	case "todoist":
		f := filter
		if f == "" {
			f = "today | overdue"
		}
		return mustJSON(map[string]any{"action": "list", "filter": f})
	case "google_tasks":
		return mustJSON(map[string]any{"action": "list"})
	case "craft_tasks":
		return mustJSON(map[string]any{"action": "list", "scope": "active"})
	default:
		return mustJSON(map[string]any{"action": "list"})
	}
}

func (s *Skill) buildCreateInput(provider string, in tasksInput) json.RawMessage {
	switch provider {
	case "todoist":
		m := map[string]any{
			"action":  "create",
			"content": in.Content,
		}
		if in.DueString != "" {
			m["due_string"] = in.DueString
		}
		if in.DueDate != "" {
			m["due_date"] = in.DueDate
		}
		if in.Priority != "" {
			m["priority"] = in.Priority
		}
		return mustJSON(m)
	case "google_tasks":
		m := map[string]any{
			"action": "create",
			"title":  in.Content,
		}
		if in.DueDate != "" {
			m["due"] = in.DueDate
		}
		return mustJSON(m)
	case "craft_tasks":
		m := map[string]any{
			"action":  "create",
			"content": in.Content,
		}
		if in.DueDate != "" {
			m["schedule_date"] = in.DueDate
		}
		return mustJSON(m)
	default:
		return mustJSON(map[string]any{"action": "create", "content": in.Content})
	}
}

func (s *Skill) buildCompleteInput(provider, taskID string) json.RawMessage {
	switch provider {
	case "todoist":
		return mustJSON(map[string]any{"action": "complete", "task_id": taskID})
	case "google_tasks":
		return mustJSON(map[string]any{"action": "complete", "task_id": taskID})
	case "craft_tasks":
		return mustJSON(map[string]any{"action": "complete", "task_id": taskID})
	default:
		return mustJSON(map[string]any{"action": "complete", "task_id": taskID})
	}
}

func mustJSON(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}

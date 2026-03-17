package agent

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/skill"
)

// Runner executes a single sub-agent task with a simplified agentic loop.
// Unlike the main assistant, it has no storage, no history, no compression,
// no approvals, and no streaming. It runs in a fresh context with only
// the tools assigned to the agent type.
type Runner struct {
	provider     llm.Provider
	registry     *skill.Registry
	notifier     channel.StatusNotifier
	chatID       string
	logger       *zap.Logger
	allowedTools map[string]bool // runtime allowlist — only these tools can be executed
}

// NewRunner constructs a Runner.
func NewRunner(
	provider llm.Provider,
	registry *skill.Registry,
	notifier channel.StatusNotifier,
	chatID string,
	logger *zap.Logger,
) *Runner {
	return &Runner{
		provider: provider,
		registry: registry,
		notifier: notifier,
		chatID:   chatID,
		logger:   logger,
	}
}

// Run executes the sub-agent and returns a result.
// sharedTokens is a shared atomic counter decremented by each sub-agent's token usage.
// A nil sharedTokens pointer means no shared budget is tracked.
func (r *Runner) Run(ctx context.Context, spec AgentSpec, budget Budget, sharedTokens *atomic.Int64) AgentResult {
	start := time.Now()
	result := AgentResult{
		ID:   spec.ID,
		Type: spec.Type,
	}

	profiles := Profiles()
	profile, ok := profiles[spec.Type]
	if !ok {
		profile = profiles[AgentTypeGeneric]
	}

	// Build tool definitions from the registry, filtered by the agent's allowlist.
	// Also populates r.allowedTools for runtime enforcement in executeTool.
	tools := r.buildTools(spec, profile)

	// Resolve route hint: spec override → profile default → empty (use default provider).
	routeHint := spec.RouteHint
	if routeHint == "" {
		routeHint = profile.RouteHint
	}

	// Build dynamic system prompt with current time (if available from parent context).
	var dynamicPrompt string
	if currentTime := CurrentTimeFrom(ctx); currentTime != "" {
		dynamicPrompt = "## Current Time (IMPORTANT)\n" + currentTime +
			"\nYou MUST use this exact date and time as the ground truth. NEVER guess or infer the current date."
	}

	req := llm.Request{
		StaticSystemPrompt: profile.SystemPrompt,
		SystemPrompt:       dynamicPrompt,
		Message:            spec.Task,
		Tools:              tools,
		RouteHint:          routeHint,
	}

	maxTurns := budget.EffectiveMaxTurns()
	var lastResp llm.Response

	for turn := 0; turn < maxTurns; turn++ {
		// Check context cancellation.
		if ctx.Err() != nil {
			result.Err = ctx.Err()
			break
		}

		// Soft budget check: if the shared budget is already depleted, stop early.
		// This is a best-effort check — multiple agents may pass this check
		// concurrently before any deducts tokens, causing the budget to be
		// overrun by up to (N_agents - 1) × tokens_per_call. This is by design:
		// a hard reservation pattern would add mutex contention without meaningful
		// benefit given the typical 3-5 agent parallelism level.
		if sharedTokens != nil && sharedTokens.Load() <= 0 {
			result.Err = ErrBudgetExhausted
			break
		}

		var err error
		lastResp, err = r.provider.Complete(ctx, req)
		if err != nil {
			result.Err = fmt.Errorf("LLM call failed (turn %d): %w", turn, err)
			break
		}

		// Track token usage.
		turnTokens := lastResp.Usage.InputTokens + lastResp.Usage.OutputTokens
		result.Tokens += turnTokens
		result.Turns = turn + 1

		// Deduct from shared budget.
		if sharedTokens != nil {
			if sharedTokens.Add(-turnTokens) <= 0 {
				// Budget exhausted after this call — use whatever we got.
				result.Output = lastResp.Content
				result.Err = ErrBudgetExhausted
				break
			}
		}

		// Emit progress notification.
		r.emitStatus(ctx, EventAgentProgress, map[string]string{
			"agent_id": spec.ID,
			"turn":     fmt.Sprintf("%d", turn+1),
		})

		// No tool calls — this is the final text response.
		if len(lastResp.ToolCalls) == 0 {
			result.Output = lastResp.Content
			break
		}

		// Execute tool calls.
		exchange := llm.ToolExchange{
			AssistantText: lastResp.Content,
			ToolCalls:     lastResp.ToolCalls,
		}

		for _, tc := range lastResp.ToolCalls {
			toolResult := r.executeTool(ctx, tc)
			exchange.Results = append(exchange.Results, toolResult)
		}

		req.ToolExchanges = append(req.ToolExchanges, exchange)
	}

	// If we exhausted turns without a final text response, use whatever we have.
	if result.Output == "" && lastResp.Content != "" {
		result.Output = lastResp.Content
	}

	result.Duration = time.Since(start)
	return result
}

// buildTools constructs the tool definitions available to this agent.
func (r *Runner) buildTools(spec AgentSpec, profile AgentTypeProfile) []llm.ToolDefinition {
	// Determine the allowlist.
	allowlist := spec.Tools
	if len(allowlist) == 0 {
		allowlist = profile.DefaultTools
	}

	// Build a lookup set if we have an allowlist.
	var allowed map[string]bool
	if len(allowlist) > 0 {
		allowed = make(map[string]bool, len(allowlist))
		for _, name := range allowlist {
			allowed[name] = true
		}
	}

	r.allowedTools = make(map[string]bool)

	var tools []llm.ToolDefinition
	for _, s := range r.registry.EnabledSkills() {
		schema := s.InputSchema()
		if schema == nil {
			continue // text-only skills don't become tools
		}

		// Never allow the orchestrate tool in sub-agents (belt-and-suspenders alongside depth check).
		if s.Name() == "orchestrate" {
			continue
		}

		// Skip skills that require approval (e.g., shell_exec with ApprovalManual).
		// Sub-agents bypass the approval flow, so they must not have access to
		// approval-gated skills in the first place.
		if ad, ok := s.(skill.ApprovalDeclarer); ok && ad.ApprovalLevel() > skill.ApprovalAuto {
			continue
		}

		// Apply allowlist filter.
		if allowed != nil && !allowed[s.Name()] {
			continue
		}

		r.allowedTools[s.Name()] = true
		tools = append(tools, llm.ToolDefinition{
			Name:        s.Name(),
			Description: s.Description(),
			InputSchema: schema,
		})
	}

	return tools
}

// executeTool runs a single tool call via the skill registry.
// Sub-agents skip approval checks — they run in trusted context.
// Runtime enforcement: only tools in r.allowedTools (set by buildTools) can be executed.
func (r *Runner) executeTool(ctx context.Context, tc llm.ToolCall) llm.ToolResult {
	// Runtime allowlist enforcement — defense in depth against hallucinated tool calls.
	if !r.allowedTools[tc.Name] {
		r.logger.Warn("sub-agent: tool call blocked by allowlist",
			zap.String("skill", tc.Name))
		return llm.ToolResult{
			ToolCallID: tc.ID,
			Content:    fmt.Sprintf("error: tool %q is not available to this agent", tc.Name),
			IsError:    true,
		}
	}

	s, ok := r.registry.Get(tc.Name)
	if !ok {
		r.logger.Warn("sub-agent: unknown or disabled skill", zap.String("skill", tc.Name))
		return llm.ToolResult{
			ToolCallID: tc.ID,
			Content:    fmt.Sprintf("error: unknown skill %q", tc.Name),
			IsError:    true,
		}
	}

	output, err := s.Execute(ctx, tc.Input)
	if err != nil {
		r.logger.Error("sub-agent: skill execution failed",
			zap.String("skill", tc.Name), zap.Error(err))
		return llm.ToolResult{
			ToolCallID: tc.ID,
			Content:    fmt.Sprintf("error: %v", err),
			IsError:    true,
		}
	}

	return llm.ToolResult{
		ToolCallID: tc.ID,
		Content:    output,
	}
}

// emitStatus sends a status event if a notifier is available.
func (r *Runner) emitStatus(ctx context.Context, eventType string, data map[string]string) {
	if r.notifier == nil {
		return
	}
	_ = r.notifier.NotifyStatus(ctx, r.chatID, channel.StatusEvent{
		Type: eventType,
		Data: data,
	})
}

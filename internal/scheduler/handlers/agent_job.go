package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/agent"
	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/eventbus"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/storage"
)

// TaskTypeAgentJob is the task type for user-defined scheduled LLM jobs.
const TaskTypeAgentJob = "agent.job"

// jobTaskTimeout bounds the wall-clock of a single agent-job run so a long or
// stuck agentic loop can't hold a worker slot indefinitely.
const jobTaskTimeout = 5 * time.Minute

// jobMaxTurns caps the agentic loop iterations per job run.
const jobMaxTurns = 8

// wakeGateSkipSentinel: if the cheap wake-gate response contains this token (and
// not "RUN"), the expensive run is skipped this cycle.
const wakeGateSkipSentinel = "SKIP"

// jobSystemPrompt constrains unattended scheduled runs to read-and-report only.
const jobSystemPrompt = `You are running an UNATTENDED scheduled job on behalf of a user. ` +
	`Use the available tools to gather the information the task asks for, then produce a concise, ` +
	`well-formatted result the user can read at a glance. ` +
	`STRICT RULES: This is a background task with no human present to confirm anything. ` +
	`ONLY read and report. NEVER create, modify, send, delete, or otherwise change anything ` +
	`(no sending emails, no creating/editing/deleting calendar events or tasks, no purchases). ` +
	`If the task seems to require a modifying action, do the read-only part and note what you did not do.`

// jobToolAllowlist is the curated set of read-leaning tools a scheduled job may
// use. Mutation-only, meta, and recursion tools are deliberately excluded;
// approval-gated tools (shell_exec, docker) are also filtered by the Runner.
// Integration tools that can write (calendar/tasks/todoist) are included so
// "check my calendar and summarize" works, but the system prompt forbids writes.
var jobToolAllowlist = []string{
	"datetime", "recall", "session_search", "list_insights", "pdf_read",
	"websearch", "webfetch", "weather", "openweathermap", "exchange_rate", "geolocation",
	"google_calendar", "google_mail", "google_tasks", "google_contacts",
	"tasks", "todoist", "craft_read", "craft_search", "craft_tasks",
}

type agentJobPayload struct {
	JobID          int64  `json:"job_id"`
	JobName        string `json:"job_name"`
	Prompt         string `json:"prompt"`
	DeliveryChatID string `json:"delivery_chat_id"`
	UserID         string `json:"user_id"`          // "" = legacy admin-global job
	Model          string `json:"model"`            // routing hint; "" = default provider
	WakeGatePrompt string `json:"wake_gate_prompt"` // optional cheap pre-check
	Timezone       string `json:"timezone"`         // IANA tz for date grounding
}

// AgentJobHandler executes user-defined prompts as scheduled tasks. For
// user-owned jobs it runs a full agentic loop (tools + user memory) scoped to the
// owning user; legacy admin-global jobs (UserID == "") run as a bare prompt.
type AgentJobHandler struct {
	store    storage.Repository
	provider llm.Provider
	registry *skill.Registry
	bus      *eventbus.Bus
	sender   channel.MessageSender
	logger   *zap.Logger
}

// NewAgentJobHandler constructs the handler. provider should be the routing
// provider so per-job RouteHints (model / cheap wake-gate) resolve.
func NewAgentJobHandler(store storage.Repository, provider llm.Provider, registry *skill.Registry, bus *eventbus.Bus, sender channel.MessageSender, logger *zap.Logger) *AgentJobHandler {
	return &AgentJobHandler{store: store, provider: provider, registry: registry, bus: bus, sender: sender, logger: logger}
}

// Type returns the task type this handler serves.
func (h *AgentJobHandler) Type() string { return TaskTypeAgentJob }

// Handle runs one firing of an agent job.
func (h *AgentJobHandler) Handle(ctx context.Context, payload string) (string, error) {
	var p agentJobPayload
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return "", fmt.Errorf("invalid payload: %w", err)
	}

	// Bound wall-clock so a stuck job can't hold the worker forever.
	ctx, cancel := context.WithTimeout(ctx, jobTaskTimeout)
	defer cancel()

	h.logger.Info("executing agent job",
		zap.Int64("job_id", p.JobID), zap.String("name", p.JobName),
		zap.String("user_id", shortID(p.UserID)), zap.Bool("agentic", p.UserID != ""))

	// Load the owning user's recent memory (only for user-scoped jobs).
	memoryBlock := ""
	if p.UserID != "" {
		memoryBlock = h.loadUserMemory(ctx, p.UserID)
	}

	// Wake-gate: a cheap pre-check that can skip this cycle. Reasons only over the
	// memory block (no tools), so it is best for memory-based conditions.
	if strings.TrimSpace(p.WakeGatePrompt) != "" {
		if skipRun := h.wakeGateSkips(ctx, p, memoryBlock); skipRun {
			h.logger.Info("agent job skipped by wake-gate", zap.Int64("job_id", p.JobID))
			return `{"status":"skipped_by_wake_gate"}`, nil
		}
	}

	content, err := h.run(ctx, p, memoryBlock)
	if err != nil {
		return "", err
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return `{"status":"empty_response"}`, nil
	}

	if p.DeliveryChatID != "" && h.sender != nil {
		msg := fmt.Sprintf("**%s**\n\n%s", p.JobName, content)
		if sendErr := h.sender.SendMessage(ctx, p.DeliveryChatID, msg); sendErr != nil {
			h.logger.Error("failed to deliver agent job result", zap.Int64("job_id", p.JobID), zap.Error(sendErr))
		}
	}

	result, err := json.Marshal(map[string]string{"status": "completed", "preview": truncate(content, 200)})
	if err != nil {
		return `{"status":"completed"}`, nil
	}
	return string(result), nil
}

// run executes the job prompt. User-scoped jobs use a full agentic loop with the
// read-only tool allowlist and user context; legacy jobs use a bare completion.
func (h *AgentJobHandler) run(ctx context.Context, p agentJobPayload, memoryBlock string) (string, error) {
	if p.UserID == "" || h.registry == nil {
		// Legacy admin-global job (or no registry wired): bare prompt, no tools.
		resp, err := h.provider.Complete(ctx, llm.Request{
			SystemPrompt: "You are a helpful assistant executing a scheduled task. Be concise and actionable.",
			Message:      p.Prompt,
		})
		if err != nil {
			return "", fmt.Errorf("agent job LLM call: %w", err)
		}
		return resp.Content, nil
	}

	// User-scoped agentic execution.
	runCtx := h.userContext(ctx, p)
	runner := agent.NewRunner(h.provider, h.registry, nil, h.bus, p.DeliveryChatID, h.logger)
	runner.SetUserID(p.UserID)

	task := p.Prompt
	if memoryBlock != "" {
		task = memoryBlock + "\n\n## Task\n" + p.Prompt
	}

	spec := agent.AgentSpec{
		ID:           fmt.Sprintf("job_%d", p.JobID),
		Type:         agent.AgentTypeGeneric,
		Task:         task,
		SystemPrompt: jobSystemPrompt,
		RouteHint:    p.Model, // "" → default provider
		Tools:        jobToolAllowlist,
	}
	res := runner.Run(runCtx, spec, agent.Budget{MaxTurns: jobMaxTurns, Timeout: jobTaskTimeout}, nil)
	if res.Err != nil {
		return "", fmt.Errorf("agent job run: %w", res.Err)
	}
	return res.Output, nil
}

// userContext enriches ctx with the identity/role/time keys the user-scoped
// skills (calendar, recall, …) require under the scheduler (no incoming message).
func (h *AgentJobHandler) userContext(ctx context.Context, p agentJobPayload) context.Context {
	ctx = skill.WithUserID(ctx, p.UserID)
	ctx = skill.WithUserRole(ctx, string(domain.RoleUser)) // jobs never run with admin privileges
	if p.DeliveryChatID != "" {
		ctx = skill.WithChatID(ctx, p.DeliveryChatID)
	}
	ctx = agent.WithCurrentTime(ctx, formatNow(p.Timezone))
	return ctx
}

// wakeGateSkips runs the cheap pre-check and reports whether the run should skip.
func (h *AgentJobHandler) wakeGateSkips(ctx context.Context, p agentJobPayload, memoryBlock string) bool {
	prompt := "You are a gate deciding whether a scheduled task is worth running right now.\n"
	if memoryBlock != "" {
		prompt += memoryBlock + "\n"
	}
	prompt += "Condition to evaluate: " + p.WakeGatePrompt +
		"\n\nAnswer with exactly RUN if the task should run now, or SKIP if it should be skipped this time."

	resp, err := h.provider.Complete(ctx, llm.Request{
		SystemPrompt: "Answer with a single word: RUN or SKIP.",
		Message:      prompt,
		RouteHint:    llm.RouteHintCheap,
	})
	if err != nil {
		// Fail open: if the gate errors, run the job rather than silently dropping it.
		h.logger.Warn("wake-gate check failed, running anyway", zap.Int64("job_id", p.JobID), zap.Error(err))
		return false
	}
	answer := strings.ToUpper(strings.TrimSpace(resp.Content))
	return strings.Contains(answer, wakeGateSkipSentinel) && !strings.Contains(answer, "RUN")
}

// loadUserMemory builds a compact context block from the user's recent facts and
// insights, for prompt grounding (kept small; prepended to the Task, not the
// rune-clamped system prompt).
func (h *AgentJobHandler) loadUserMemory(ctx context.Context, userID string) string {
	facts, err := h.store.GetRecentFactsByUser(ctx, userID, 15)
	if err != nil {
		facts = nil
	}
	insights, err := h.store.GetRecentInsightsByUser(ctx, userID, 5)
	if err != nil {
		insights = nil
	}
	if len(facts) == 0 && len(insights) == 0 {
		return ""
	}
	var b strings.Builder
	if len(facts) > 0 {
		b.WriteString("## Known facts about the user\n")
		for i := range facts {
			fmt.Fprintf(&b, "- %s\n", strings.TrimSpace(facts[i].Content))
		}
	}
	if len(insights) > 0 {
		b.WriteString("## Recent insights\n")
		for i := range insights {
			fmt.Fprintf(&b, "- %s\n", strings.TrimSpace(insights[i].Content))
		}
	}
	return strings.TrimSpace(b.String())
}

// formatNow renders the current time in the given IANA timezone (UTC fallback)
// for date-grounding the job prompt.
func formatNow(tz string) string {
	loc := time.UTC
	if tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	return time.Now().In(loc).Format("2006-01-02 Monday 15:04 (MST)")
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func truncate(s string, maxLen int) string {
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	return string([]rune(s)[:maxLen]) + "..."
}

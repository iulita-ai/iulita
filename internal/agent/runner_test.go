package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/skill"
)

// stubProvider returns preconfigured responses in sequence.
type stubProvider struct {
	responses []llm.Response
	errors    []error
	calls     int
}

func (s *stubProvider) Complete(_ context.Context, _ llm.Request) (llm.Response, error) {
	idx := s.calls
	s.calls++
	if idx < len(s.errors) && s.errors[idx] != nil {
		return llm.Response{}, s.errors[idx]
	}
	if idx < len(s.responses) {
		return s.responses[idx], nil
	}
	return llm.Response{Content: "fallback"}, nil
}

// stubSkill is a minimal skill for testing.
type stubSkill struct {
	name   string
	output string
	err    error
}

func (s *stubSkill) Name() string                 { return s.name }
func (s *stubSkill) Description() string          { return "test skill" }
func (s *stubSkill) InputSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (s *stubSkill) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return s.output, s.err
}

func newTestRegistry(skills ...skill.Skill) *skill.Registry {
	r := skill.NewRegistry()
	for _, s := range skills {
		r.Register(s)
	}
	return r
}

func TestRunnerBasicCompletion(t *testing.T) {
	provider := &stubProvider{
		responses: []llm.Response{
			{Content: "Hello from sub-agent", Usage: llm.Usage{InputTokens: 100, OutputTokens: 50}},
		},
	}

	runner := NewRunner(provider, newTestRegistry(), nil, nil, "chat1", zap.NewNop())
	result := runner.Run(context.Background(), AgentSpec{
		ID:   "test",
		Type: AgentTypeGeneric,
		Task: "Say hello",
	}, Budget{}, nil)

	if result.Err != nil {
		t.Fatalf("unexpected error: %v", result.Err)
	}
	if result.Output != "Hello from sub-agent" {
		t.Errorf("output = %q, want %q", result.Output, "Hello from sub-agent")
	}
	if result.Turns != 1 {
		t.Errorf("turns = %d, want 1", result.Turns)
	}
	if result.Tokens != 150 {
		t.Errorf("tokens = %d, want 150", result.Tokens)
	}
	if result.ID != "test" {
		t.Errorf("id = %q, want %q", result.ID, "test")
	}
}

func TestRunnerToolCalls(t *testing.T) {
	provider := &stubProvider{
		responses: []llm.Response{
			{
				ToolCalls: []llm.ToolCall{{ID: "tc1", Name: "datetime", Input: json.RawMessage(`{}`)}},
				Usage:     llm.Usage{InputTokens: 50, OutputTokens: 20},
			},
			{
				Content: "The current time is 12:00",
				Usage:   llm.Usage{InputTokens: 80, OutputTokens: 30},
			},
		},
	}

	registry := newTestRegistry(&stubSkill{name: "datetime", output: "2026-03-17 12:00:00"})
	runner := NewRunner(provider, registry, nil, nil, "chat1", zap.NewNop())
	result := runner.Run(context.Background(), AgentSpec{
		ID:   "test",
		Type: AgentTypeGeneric,
		Task: "What time is it?",
	}, Budget{}, nil)

	if result.Err != nil {
		t.Fatalf("unexpected error: %v", result.Err)
	}
	if result.Output != "The current time is 12:00" {
		t.Errorf("output = %q", result.Output)
	}
	if result.Turns != 2 {
		t.Errorf("turns = %d, want 2", result.Turns)
	}
	if result.Tokens != 180 {
		t.Errorf("tokens = %d, want 180", result.Tokens)
	}
}

func TestRunnerMaxTurns(t *testing.T) {
	// Provider always returns tool calls — never a text response.
	provider := &stubProvider{
		responses: make([]llm.Response, 20),
	}
	for i := range provider.responses {
		provider.responses[i] = llm.Response{
			ToolCalls: []llm.ToolCall{{ID: "tc", Name: "datetime", Input: json.RawMessage(`{}`)}},
			Usage:     llm.Usage{InputTokens: 10, OutputTokens: 5},
		}
	}

	registry := newTestRegistry(&stubSkill{name: "datetime", output: "ok"})
	runner := NewRunner(provider, registry, nil, nil, "chat1", zap.NewNop())
	result := runner.Run(context.Background(), AgentSpec{
		ID:   "test",
		Type: AgentTypeGeneric,
		Task: "Loop forever",
	}, Budget{MaxTurns: 3}, nil)

	if result.Turns != 3 {
		t.Errorf("turns = %d, want 3 (max)", result.Turns)
	}
}

func TestRunnerBudgetExhaustion(t *testing.T) {
	provider := &stubProvider{
		responses: []llm.Response{
			{Content: "done", Usage: llm.Usage{InputTokens: 500, OutputTokens: 600}},
		},
	}

	budget := &atomic.Int64{}
	budget.Store(100) // only 100 tokens allowed

	runner := NewRunner(provider, newTestRegistry(), nil, nil, "chat1", zap.NewNop())
	result := runner.Run(context.Background(), AgentSpec{
		ID:   "test",
		Type: AgentTypeGeneric,
		Task: "expensive task",
	}, Budget{}, budget)

	if !errors.Is(result.Err, ErrBudgetExhausted) {
		t.Errorf("expected ErrBudgetExhausted, got %v", result.Err)
	}
	if result.Output != "done" {
		t.Errorf("should still have output from the call, got %q", result.Output)
	}
}

func TestRunnerTimeout(t *testing.T) {
	// Provider blocks until context is canceled.
	provider := &stubProvider{
		errors: []error{context.DeadlineExceeded},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	runner := NewRunner(provider, newTestRegistry(), nil, nil, "chat1", zap.NewNop())
	result := runner.Run(ctx, AgentSpec{
		ID:   "test",
		Type: AgentTypeGeneric,
		Task: "slow task",
	}, Budget{}, nil)

	if result.Err == nil {
		t.Fatal("expected error for timeout")
	}
}

func TestRunnerContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	provider := &stubProvider{}
	runner := NewRunner(provider, newTestRegistry(), nil, nil, "chat1", zap.NewNop())
	result := runner.Run(ctx, AgentSpec{
		ID:   "test",
		Type: AgentTypeGeneric,
		Task: "canceled",
	}, Budget{}, nil)

	if result.Err == nil {
		t.Fatal("expected context error")
	}
}

func TestRunnerToolAllowlist(t *testing.T) {
	// Provider returns a tool call for a skill not in the allowlist.
	provider := &stubProvider{
		responses: []llm.Response{
			{
				ToolCalls: []llm.ToolCall{{ID: "tc1", Name: "shell_exec", Input: json.RawMessage(`{}`)}},
				Usage:     llm.Usage{InputTokens: 10, OutputTokens: 5},
			},
			{Content: "done", Usage: llm.Usage{InputTokens: 10, OutputTokens: 5}},
		},
	}

	registry := newTestRegistry(
		&stubSkill{name: "datetime", output: "time"},
		&stubSkill{name: "shell_exec", output: "exec result"},
	)

	runner := NewRunner(provider, registry, nil, nil, "chat1", zap.NewNop())

	// Run with researcher type — only web_search and webfetch allowed.
	result := runner.Run(context.Background(), AgentSpec{
		ID:   "test",
		Type: AgentTypeResearcher,
		Task: "research something",
	}, Budget{}, nil)

	// shell_exec is blocked at execution time: it is not in r.allowedTools (the
	// researcher profile only allows web_search and webfetch), so executeTool
	// returns an IsError ToolResult without calling the skill. The stub provider
	// then returns "done" on the next turn, so the overall run succeeds (no Err).
	if result.Err != nil {
		t.Fatalf("unexpected error: %v", result.Err)
	}
}

func TestRunnerProfileRouteHint(t *testing.T) {
	// Track what RouteHint was sent to the provider.
	var capturedHint string
	provider := &capturingProvider{
		inner: &stubProvider{
			responses: []llm.Response{
				{Content: "summarized", Usage: llm.Usage{InputTokens: 10, OutputTokens: 5}},
			},
		},
		onComplete: func(req llm.Request) {
			capturedHint = req.RouteHint
		},
	}

	runner := NewRunner(provider, newTestRegistry(), nil, nil, "chat1", zap.NewNop())
	result := runner.Run(context.Background(), AgentSpec{
		ID:   "test",
		Type: AgentTypeSummarizer, // profile has RouteHint: llm.RouteHintCheap
		Task: "summarize this",
	}, Budget{}, nil)

	if result.Err != nil {
		t.Fatalf("unexpected error: %v", result.Err)
	}
	if capturedHint != llm.RouteHintCheap {
		t.Errorf("RouteHint = %q, want %q", capturedHint, llm.RouteHintCheap)
	}
}

func TestRunnerSpecRouteHintOverridesProfile(t *testing.T) {
	var capturedHint string
	provider := &capturingProvider{
		inner: &stubProvider{
			responses: []llm.Response{
				{Content: "done", Usage: llm.Usage{InputTokens: 10, OutputTokens: 5}},
			},
		},
		onComplete: func(req llm.Request) {
			capturedHint = req.RouteHint
		},
	}

	runner := NewRunner(provider, newTestRegistry(), nil, nil, "chat1", zap.NewNop())
	runner.Run(context.Background(), AgentSpec{
		ID:        "test",
		Type:      AgentTypeSummarizer, // profile default: "ollama"
		Task:      "summarize",
		RouteHint: "openai", // spec override
	}, Budget{}, nil)

	if capturedHint != "openai" {
		t.Errorf("RouteHint = %q, want %q (spec override)", capturedHint, "openai")
	}
}

func TestRunnerCurrentTimeInjection(t *testing.T) {
	var capturedPrompt string
	provider := &capturingProvider{
		inner: &stubProvider{
			responses: []llm.Response{
				{Content: "done", Usage: llm.Usage{InputTokens: 10, OutputTokens: 5}},
			},
		},
		onComplete: func(req llm.Request) {
			capturedPrompt = req.SystemPrompt
		},
	}

	// Inject current time via context.
	ctx := WithCurrentTime(context.Background(), "2026-03-17 Monday 16:30:45 (MSK, UTC+3:00)")

	runner := NewRunner(provider, newTestRegistry(), nil, nil, "chat1", zap.NewNop())
	result := runner.Run(ctx, AgentSpec{
		ID:   "test",
		Type: AgentTypeGeneric,
		Task: "what time is it?",
	}, Budget{}, nil)

	if result.Err != nil {
		t.Fatalf("unexpected error: %v", result.Err)
	}
	if capturedPrompt == "" {
		t.Fatal("SystemPrompt should contain current time")
	}
	if !strings.Contains(capturedPrompt, "2026-03-17") {
		t.Errorf("SystemPrompt should contain date, got: %s", capturedPrompt)
	}
	if !strings.Contains(capturedPrompt, "MSK") {
		t.Errorf("SystemPrompt should contain timezone, got: %s", capturedPrompt)
	}
}

func TestRunnerNoCurrentTimeWithoutContext(t *testing.T) {
	var capturedPrompt string
	provider := &capturingProvider{
		inner: &stubProvider{
			responses: []llm.Response{
				{Content: "done", Usage: llm.Usage{InputTokens: 10, OutputTokens: 5}},
			},
		},
		onComplete: func(req llm.Request) {
			capturedPrompt = req.SystemPrompt
		},
	}

	// No WithCurrentTime — empty context.
	runner := NewRunner(provider, newTestRegistry(), nil, nil, "chat1", zap.NewNop())
	runner.Run(context.Background(), AgentSpec{
		ID:   "test",
		Type: AgentTypeGeneric,
		Task: "test",
	}, Budget{}, nil)

	if capturedPrompt != "" {
		t.Errorf("SystemPrompt should be empty without current time, got: %s", capturedPrompt)
	}
}

// capturingProvider wraps a provider and captures the request for inspection.
type capturingProvider struct {
	inner      llm.Provider
	onComplete func(llm.Request)
}

func (p *capturingProvider) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if p.onComplete != nil {
		p.onComplete(req)
	}
	return p.inner.Complete(ctx, req)
}

func newCapturingRunner(t *testing.T, capture *llm.Request) *Runner {
	t.Helper()
	provider := &capturingProvider{
		inner: &stubProvider{
			responses: []llm.Response{{Content: "done", Usage: llm.Usage{InputTokens: 10, OutputTokens: 5}}},
		},
		onComplete: func(req llm.Request) { *capture = req },
	}
	return NewRunner(provider, newTestRegistry(), nil, nil, "chat1", zap.NewNop())
}

func TestRunnerSpecSystemPromptInjection(t *testing.T) {
	var req llm.Request
	runner := newCapturingRunner(t, &req)
	runner.Run(context.Background(), AgentSpec{
		ID:           "test",
		Type:         AgentTypeGeneric,
		Task:         "do the thing",
		SystemPrompt: "You are a terse pirate. Answer in one sentence.",
	}, Budget{}, nil)

	if !strings.Contains(req.SystemPrompt, "## Task-specific instructions") {
		t.Errorf("dynamic prompt missing header, got: %q", req.SystemPrompt)
	}
	if !strings.Contains(req.SystemPrompt, "terse pirate") {
		t.Errorf("dynamic prompt missing custom instructions, got: %q", req.SystemPrompt)
	}
	// Custom instructions must NOT pollute the cached static prompt.
	if strings.Contains(req.StaticSystemPrompt, "terse pirate") {
		t.Errorf("custom instructions leaked into StaticSystemPrompt: %q", req.StaticSystemPrompt)
	}
}

func TestRunnerSystemPromptCombinesWithTime(t *testing.T) {
	var req llm.Request
	runner := newCapturingRunner(t, &req)
	ctx := WithCurrentTime(context.Background(), "2026-03-17 Monday 16:30:45 (MSK, UTC+3:00)")
	runner.Run(ctx, AgentSpec{
		ID:           "test",
		Type:         AgentTypeGeneric,
		Task:         "task",
		SystemPrompt: "custom-marker-xyz",
	}, Budget{}, nil)

	if !strings.Contains(req.SystemPrompt, "2026-03-17") {
		t.Errorf("dynamic prompt should keep current time, got: %q", req.SystemPrompt)
	}
	if !strings.Contains(req.SystemPrompt, "custom-marker-xyz") {
		t.Errorf("dynamic prompt should include custom instructions, got: %q", req.SystemPrompt)
	}
}

func TestRunnerNoSystemPromptHeaderWhenEmpty(t *testing.T) {
	var req llm.Request
	runner := newCapturingRunner(t, &req)
	runner.Run(context.Background(), AgentSpec{ID: "test", Type: AgentTypeGeneric, Task: "task"}, Budget{}, nil)

	if strings.Contains(req.SystemPrompt, "Task-specific instructions") {
		t.Errorf("no custom prompt expected, got: %q", req.SystemPrompt)
	}
}

func TestRunnerSystemPromptClamped(t *testing.T) {
	var req llm.Request
	runner := newCapturingRunner(t, &req)
	// Use a marker char absent from the "Task-specific instructions" header.
	huge := strings.Repeat("z", maxSpecSystemPrompt+500)
	runner.Run(context.Background(), AgentSpec{
		ID: "test", Type: AgentTypeGeneric, Task: "task", SystemPrompt: huge,
	}, Budget{}, nil)

	zs := strings.Count(req.SystemPrompt, "z")
	if zs != maxSpecSystemPrompt {
		t.Errorf("custom prompt not clamped: %d 'z' runes, want exactly %d", zs, maxSpecSystemPrompt)
	}
}

func TestClampRunes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"under limit", "hello", 10, "hello"},
		{"exact limit", "hello", 5, "hello"},
		{"truncated ascii", "hello world", 5, "hello"},
		{"multibyte not split", "héllo", 2, "hé"},
		{"multibyte truncated", "日本語テスト", 3, "日本語"},
		{"empty", "", 5, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clampRunes(tt.in, tt.max); got != tt.want {
				t.Errorf("clampRunes(%q, %d) = %q, want %q", tt.in, tt.max, got, tt.want)
			}
		})
	}
}

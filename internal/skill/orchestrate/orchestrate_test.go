package orchestrate

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/agent"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/skill"
)

type testProvider struct {
	content string
}

func (p *testProvider) Complete(_ context.Context, _ llm.Request) (llm.Response, error) {
	return llm.Response{
		Content: p.content,
		Usage:   llm.Usage{InputTokens: 50, OutputTokens: 30},
	}, nil
}

func TestSkillMetadata(t *testing.T) {
	s := New(nil, nil, nil, zap.NewNop())
	if s.Name() != "orchestrate" {
		t.Errorf("Name() = %q, want %q", s.Name(), "orchestrate")
	}
	if s.InputSchema() == nil {
		t.Error("InputSchema() should not be nil")
	}
	if s.Description() == "" {
		t.Error("Description() should not be empty")
	}
}

func TestSkillBasicExecution(t *testing.T) {
	provider := &testProvider{content: "agent result"}
	registry := skill.NewRegistry()
	s := New(provider, registry, nil, zap.NewNop())

	input := `{
		"agents": [
			{"id": "a1", "type": "generic", "task": "do something"},
			{"id": "a2", "type": "researcher", "task": "search for info"}
		]
	}`

	ctx := skill.WithChatID(context.Background(), "chat1")
	result, err := s.Execute(ctx, json.RawMessage(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should contain both agent results.
	if result == "" {
		t.Error("result should not be empty")
	}
	if !contains(result, "a1") || !contains(result, "a2") {
		t.Errorf("result should contain agent IDs, got: %s", result)
	}
}

func TestSkillDepthRejection(t *testing.T) {
	s := New(&testProvider{content: "ok"}, skill.NewRegistry(), nil, zap.NewNop())

	input := `{"agents": [{"id": "a1", "type": "generic", "task": "test"}]}`

	// Set depth to MaxDepth — should be rejected.
	ctx := agent.WithDepth(context.Background(), agent.MaxDepth)
	ctx = skill.WithChatID(ctx, "chat1")

	result, err := s.Execute(ctx, json.RawMessage(input))
	if err != nil {
		t.Fatalf("should return error as string, not Go error: %v", err)
	}
	if !contains(result, "error") || !contains(result, "depth") {
		t.Errorf("expected depth rejection message, got: %s", result)
	}
}

func TestSkillEmptyAgents(t *testing.T) {
	s := New(nil, nil, nil, zap.NewNop())

	input := `{"agents": []}`
	ctx := skill.WithChatID(context.Background(), "chat1")
	result, err := s.Execute(ctx, json.RawMessage(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(result, "error") {
		t.Errorf("expected error for empty agents, got: %s", result)
	}
}

func TestSkillInvalidInput(t *testing.T) {
	s := New(nil, nil, nil, zap.NewNop())

	ctx := skill.WithChatID(context.Background(), "chat1")
	_, err := s.Execute(ctx, json.RawMessage(`{invalid`))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestSkillAutoAssignIDs(t *testing.T) {
	provider := &testProvider{content: "ok"}
	s := New(provider, skill.NewRegistry(), nil, zap.NewNop())

	input := `{"agents": [{"type": "generic", "task": "no id"}]}`
	ctx := skill.WithChatID(context.Background(), "chat1")
	result, err := s.Execute(ctx, json.RawMessage(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(result, "agent_1") {
		t.Errorf("expected auto-assigned ID 'agent_1', got: %s", result)
	}
}

func TestSkillConfigReload(t *testing.T) {
	s := New(nil, nil, nil, zap.NewNop())

	s.OnConfigChanged("skills.orchestrate.max_agents", "3")
	if s.maxAgents != 3 {
		t.Errorf("maxAgents = %d, want 3", s.maxAgents)
	}

	s.OnConfigChanged("skills.orchestrate.timeout", "30s")
	if s.timeout.Seconds() != 30 {
		t.Errorf("timeout = %v, want 30s", s.timeout)
	}

	s.OnConfigChanged("skills.orchestrate.max_tokens", "100000")
	if s.maxTokens != 100000 {
		t.Errorf("maxTokens = %d, want 100000", s.maxTokens)
	}
}

func TestSkillRequestTimeout(t *testing.T) {
	s := New(nil, nil, nil, zap.NewNop())

	// Default should be 1 hour.
	if got := s.RequestTimeout(); got != time.Hour {
		t.Errorf("default RequestTimeout = %v, want 1h", got)
	}

	// Config override.
	s.OnConfigChanged("skills.orchestrate.request_timeout", "30m")
	if got := s.RequestTimeout(); got != 30*time.Minute {
		t.Errorf("RequestTimeout after config = %v, want 30m", got)
	}
}

func TestSkillRequestTimeoutCap(t *testing.T) {
	s := New(nil, nil, nil, zap.NewNop())

	// Set absurdly high value — should be capped at 4h.
	s.OnConfigChanged("skills.orchestrate.request_timeout", "1000h")
	if got := s.RequestTimeout(); got != 4*time.Hour {
		t.Errorf("RequestTimeout = %v, want 4h (cap)", got)
	}
}

func TestSkillRequestTimeoutRevert(t *testing.T) {
	s := New(nil, nil, nil, zap.NewNop())

	// Set custom, then revert (empty value = delete from DB).
	s.OnConfigChanged("skills.orchestrate.request_timeout", "15m")
	if got := s.RequestTimeout(); got != 15*time.Minute {
		t.Errorf("RequestTimeout = %v, want 15m", got)
	}

	s.OnConfigChanged("skills.orchestrate.request_timeout", "")
	if got := s.RequestTimeout(); got != time.Hour {
		t.Errorf("RequestTimeout after revert = %v, want 1h (default)", got)
	}
}

func TestSkillImplementsTimeoutDeclarer(t *testing.T) {
	s := New(nil, nil, nil, zap.NewNop())
	var td skill.TimeoutDeclarer = s // compile-time check
	if td.RequestTimeout() <= 0 {
		t.Error("RequestTimeout should be positive")
	}
}

func TestSkillAgentTypeMapping(t *testing.T) {
	provider := &testProvider{content: "ok"}
	s := New(provider, skill.NewRegistry(), nil, zap.NewNop())

	// Test that unknown types default to generic.
	input := `{"agents": [{"id": "a1", "type": "unknown_type", "task": "test"}]}`
	ctx := skill.WithChatID(context.Background(), "chat1")
	result, err := s.Execute(ctx, json.RawMessage(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(result, "generic") {
		t.Errorf("unknown type should map to generic, got: %s", result)
	}
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

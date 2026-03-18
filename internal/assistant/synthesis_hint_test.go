package assistant

import (
	"context"
	"encoding/json"
	"testing"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
)

// --- Test helpers ---

type funcProvider struct {
	fn func(ctx context.Context, req llm.Request) (llm.Response, error)
}

func (p *funcProvider) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	return p.fn(ctx, req)
}

func newSynthTestStore(t *testing.T) *sqlite.Store {
	t.Helper()
	store, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	if err := store.RunMigrations(context.Background()); err != nil {
		t.Fatalf("running migrations: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func newTestMsg(chatID, text string) channel.IncomingMessage {
	return channel.IncomingMessage{
		ChatID: chatID,
		UserID: "test-user",
		Text:   text,
	}
}

// --- Test skills ---

type cheapSkill struct{}

func (s *cheapSkill) Name() string                 { return "cheap_tool" }
func (s *cheapSkill) Description() string          { return "A cheap test tool" }
func (s *cheapSkill) InputSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (s *cheapSkill) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "cheap result", nil
}
func (s *cheapSkill) SynthesisRouteHint() string { return llm.RouteHintCheap }

type expensiveSkill struct{}

func (s *expensiveSkill) Name() string                 { return "expensive_tool" }
func (s *expensiveSkill) Description() string          { return "An expensive test tool" }
func (s *expensiveSkill) InputSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (s *expensiveSkill) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "expensive result", nil
}

var _ skill.SynthesisModelDeclarer = (*cheapSkill)(nil)

// --- Tests ---

func TestSynthesisRouteHint_AppliedAfterCheapSkill(t *testing.T) {
	var callHints []string
	provider := &funcProvider{
		fn: func(_ context.Context, req llm.Request) (llm.Response, error) {
			callHints = append(callHints, req.RouteHint)
			if len(callHints) == 1 {
				return llm.Response{
					ToolCalls: []llm.ToolCall{{ID: "tc1", Name: "cheap_tool", Input: json.RawMessage(`{}`)}},
					Usage:     llm.Usage{InputTokens: 100, OutputTokens: 50},
				}, nil
			}
			return llm.Response{
				Content: "synthesized",
				Usage:   llm.Usage{InputTokens: 100, OutputTokens: 50},
			}, nil
		},
	}

	registry := skill.NewRegistry()
	registry.Register(&cheapSkill{})

	asst := New(provider, newSynthTestStore(t), registry, "test", "", 200000, zap.NewNop())

	_, _ = asst.HandleMessage(context.Background(), newTestMsg("chat1", "use cheap tool"))

	if len(callHints) < 2 {
		t.Fatalf("expected at least 2 LLM calls, got %d", len(callHints))
	}
	if callHints[0] != "" {
		t.Errorf("first call RouteHint = %q, want empty", callHints[0])
	}
	if callHints[1] != llm.RouteHintCheap {
		t.Errorf("synthesis call RouteHint = %q, want %q", callHints[1], llm.RouteHintCheap)
	}
}

func TestSynthesisRouteHint_NotSetForExpensiveSkill(t *testing.T) {
	var callHints []string
	provider := &funcProvider{
		fn: func(_ context.Context, req llm.Request) (llm.Response, error) {
			callHints = append(callHints, req.RouteHint)
			if len(callHints) == 1 {
				return llm.Response{
					ToolCalls: []llm.ToolCall{{ID: "tc1", Name: "expensive_tool", Input: json.RawMessage(`{}`)}},
					Usage:     llm.Usage{InputTokens: 100, OutputTokens: 50},
				}, nil
			}
			return llm.Response{
				Content: "synthesized",
				Usage:   llm.Usage{InputTokens: 100, OutputTokens: 50},
			}, nil
		},
	}

	registry := skill.NewRegistry()
	registry.Register(&expensiveSkill{})

	asst := New(provider, newSynthTestStore(t), registry, "test", "", 200000, zap.NewNop())

	_, _ = asst.HandleMessage(context.Background(), newTestMsg("chat1", "use expensive tool"))

	if len(callHints) < 2 {
		t.Fatalf("expected at least 2 LLM calls, got %d", len(callHints))
	}
	for i, hint := range callHints {
		if hint != "" {
			t.Errorf("call %d RouteHint = %q, want empty", i, hint)
		}
	}
}

func TestSynthesisRouteHint_ResetsAcrossIterations(t *testing.T) {
	callCount := 0
	var callHints []string
	provider := &funcProvider{
		fn: func(_ context.Context, req llm.Request) (llm.Response, error) {
			callCount++
			callHints = append(callHints, req.RouteHint)
			switch callCount {
			case 1:
				return llm.Response{
					ToolCalls: []llm.ToolCall{{ID: "tc1", Name: "cheap_tool", Input: json.RawMessage(`{}`)}},
					Usage:     llm.Usage{InputTokens: 100, OutputTokens: 50},
				}, nil
			case 2:
				// Synthesis with cheap hint, but decides to call expensive_tool.
				return llm.Response{
					ToolCalls: []llm.ToolCall{{ID: "tc2", Name: "expensive_tool", Input: json.RawMessage(`{}`)}},
					Usage:     llm.Usage{InputTokens: 100, OutputTokens: 50},
				}, nil
			default:
				return llm.Response{
					Content: "done",
					Usage:   llm.Usage{InputTokens: 100, OutputTokens: 50},
				}, nil
			}
		},
	}

	registry := skill.NewRegistry()
	registry.Register(&cheapSkill{})
	registry.Register(&expensiveSkill{})

	asst := New(provider, newSynthTestStore(t), registry, "test", "", 200000, zap.NewNop())

	_, _ = asst.HandleMessage(context.Background(), newTestMsg("chat1", "multi-tool"))

	if len(callHints) < 3 {
		t.Fatalf("expected 3 LLM calls, got %d", len(callHints))
	}
	if callHints[0] != "" {
		t.Errorf("call 0 RouteHint = %q, want empty", callHints[0])
	}
	if callHints[1] != llm.RouteHintCheap {
		t.Errorf("call 1 RouteHint = %q, want %q", callHints[1], llm.RouteHintCheap)
	}
	// After expensive_tool (no hint), should reset.
	if callHints[2] != "" {
		t.Errorf("call 2 RouteHint = %q, want empty (reset)", callHints[2])
	}
}

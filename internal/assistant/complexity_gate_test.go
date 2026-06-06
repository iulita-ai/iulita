package assistant

import (
	"context"
	"encoding/json"
	"testing"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/storage"
)

// twoToolThenDone returns a provider that emits a tool call on the first N LLM
// calls, then a final text response.
func toolThenDoneProvider(toolTurns int) *funcProvider {
	calls := 0
	return &funcProvider{
		fn: func(_ context.Context, _ llm.Request) (llm.Response, error) {
			calls++
			if calls <= toolTurns {
				return llm.Response{
					ToolCalls: []llm.ToolCall{{ID: "tc", Name: "cheap_tool", Input: json.RawMessage(`{}`)}},
					Usage:     llm.Usage{InputTokens: 10, OutputTokens: 5},
				}, nil
			}
			return llm.Response{Content: "done", Usage: llm.Usage{InputTokens: 10, OutputTokens: 5}}, nil
		},
	}
}

func countReviewTasks(t *testing.T, store storage.Repository) int {
	t.Helper()
	tasks, err := store.ListTasks(context.Background(), storage.TaskFilter{Type: "skill.review", Limit: 10})
	if err != nil {
		t.Fatalf("listing tasks: %v", err)
	}
	return len(tasks)
}

func TestComplexityGate_EnqueuesAfterThreshold(t *testing.T) {
	registry := skill.NewRegistry()
	registry.Register(&cheapSkill{})

	store := newSynthTestStore(t)
	asst := New(toolThenDoneProvider(3), store, registry, "test", "", 200000, zap.NewNop())
	asst.SetSelfImprove(true, 2) // 3 tool iterations >= threshold 2

	if _, err := asst.HandleMessage(context.Background(), newTestMsg("chat1", "hard task")); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	if n := countReviewTasks(t, store); n != 1 {
		t.Fatalf("expected 1 skill.review task, got %d", n)
	}
}

func TestComplexityGate_BelowThresholdNoTask(t *testing.T) {
	registry := skill.NewRegistry()
	registry.Register(&cheapSkill{})

	store := newSynthTestStore(t)
	asst := New(toolThenDoneProvider(1), store, registry, "test", "", 200000, zap.NewNop())
	asst.SetSelfImprove(true, 3) // only 1 tool iteration < threshold 3

	if _, err := asst.HandleMessage(context.Background(), newTestMsg("chat1", "easy task")); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	if n := countReviewTasks(t, store); n != 0 {
		t.Fatalf("expected no skill.review task, got %d", n)
	}
}

func TestComplexityGate_DisabledNoTask(t *testing.T) {
	registry := skill.NewRegistry()
	registry.Register(&cheapSkill{})

	store := newSynthTestStore(t)
	asst := New(toolThenDoneProvider(3), store, registry, "test", "", 200000, zap.NewNop())
	// self-improve left disabled (default)

	if _, err := asst.HandleMessage(context.Background(), newTestMsg("chat1", "hard task")); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	if n := countReviewTasks(t, store); n != 0 {
		t.Fatalf("expected no skill.review task when disabled, got %d", n)
	}
}

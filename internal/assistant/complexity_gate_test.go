package assistant

import (
	"context"
	"encoding/json"
	"strings"
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
	tasks, err := store.ListTasks(context.Background(), storage.TaskFilter{Type: "skill.review", Limit: 100})
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

func TestComplexityGate_AtExactThreshold(t *testing.T) {
	registry := skill.NewRegistry()
	registry.Register(&cheapSkill{})

	store := newSynthTestStore(t)
	asst := New(toolThenDoneProvider(2), store, registry, "test", "", 200000, zap.NewNop())
	asst.SetSelfImprove(true, 2) // exactly 2 tool iterations == threshold 2 must trigger

	if _, err := asst.HandleMessage(context.Background(), newTestMsg("chat1", "boundary")); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if n := countReviewTasks(t, store); n != 1 {
		t.Fatalf("equality boundary should enqueue exactly 1 task, got %d", n)
	}
}

func TestComplexityGate_PayloadCarriesToolSummary(t *testing.T) {
	registry := skill.NewRegistry()
	registry.Register(&cheapSkill{})

	store := newSynthTestStore(t)
	asst := New(toolThenDoneProvider(2), store, registry, "test", "", 200000, zap.NewNop())
	asst.SetSelfImprove(true, 2)

	if _, err := asst.HandleMessage(context.Background(), newTestMsg("chat1", "hard")); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	tasks, err := store.ListTasks(context.Background(), storage.TaskFilter{Type: "skill.review", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if !strings.Contains(tasks[0].Payload, "tool_summary") || !strings.Contains(tasks[0].Payload, "cheap_tool") {
		t.Errorf("payload missing tool summary: %s", tasks[0].Payload)
	}
}

func TestComplexityGate_DedupSameTurn(t *testing.T) {
	registry := skill.NewRegistry()
	registry.Register(&cheapSkill{})
	store := newSynthTestStore(t)
	asst := New(toolThenDoneProvider(1), store, registry, "test", "", 200000, zap.NewNop())
	asst.SetSelfImprove(true, 1)

	ctx := context.Background()
	asst.maybeEnqueueSkillReview(ctx, "chat1", "user1", 3, 100, "1. cheap_tool -> ok\n")
	asst.maybeEnqueueSkillReview(ctx, "chat1", "user1", 3, 100, "1. cheap_tool -> ok\n")
	if n := countReviewTasks(t, store); n != 1 {
		t.Fatalf("same turn boundary must dedup to 1 task, got %d", n)
	}
}

func TestComplexityGate_NoTaskWhenBoundaryUnsaved(t *testing.T) {
	registry := skill.NewRegistry()
	store := newSynthTestStore(t)
	asst := New(toolThenDoneProvider(1), store, registry, "test", "", 200000, zap.NewNop())
	asst.SetSelfImprove(true, 1)

	// lastMessageID==0 means the assistant message failed to save — no boundary.
	asst.maybeEnqueueSkillReview(context.Background(), "chat1", "user1", 5, 0, "x")
	if n := countReviewTasks(t, store); n != 0 {
		t.Fatalf("must not enqueue with no turn boundary, got %d", n)
	}
}

func TestComplexityGate_PerUserRateLimit(t *testing.T) {
	registry := skill.NewRegistry()
	store := newSynthTestStore(t)
	asst := New(toolThenDoneProvider(1), store, registry, "test", "", 200000, zap.NewNop())
	asst.SetSelfImprove(true, 1)

	ctx := context.Background()
	// Distinct boundaries (avoid per-turn dedup) for the same user, past the cap.
	for i := 1; i <= maxReviewsPerUserPerHour+5; i++ {
		asst.maybeEnqueueSkillReview(ctx, "chat1", "user1", 3, int64(i), "x")
	}
	if n := countReviewTasks(t, store); n != maxReviewsPerUserPerHour {
		t.Fatalf("expected cap of %d review tasks, got %d", maxReviewsPerUserPerHour, n)
	}

	// A different user is unaffected by the first user's cap.
	asst.maybeEnqueueSkillReview(ctx, "chat2", "user2", 3, 9001, "x")
	if n := countReviewTasks(t, store); n != maxReviewsPerUserPerHour+1 {
		t.Fatalf("second user should not be rate-limited, got %d", n)
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

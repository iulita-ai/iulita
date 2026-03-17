package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/skill"
)

func TestOrchestratorParallelExecution(t *testing.T) {
	// Each agent gets its own response.
	callCount := atomic.Int32{}
	provider := &funcProvider{fn: func(ctx context.Context, req llm.Request) (llm.Response, error) {
		callCount.Add(1)
		// Simulate some work.
		time.Sleep(10 * time.Millisecond)
		return llm.Response{
			Content: "result for: " + req.Message,
			Usage:   llm.Usage{InputTokens: 50, OutputTokens: 30},
		}, nil
	}}

	orch := NewOrchestrator(provider, newTestRegistry(), nil, nil, zap.NewNop())
	specs := []AgentSpec{
		{ID: "a1", Type: AgentTypeResearcher, Task: "task 1"},
		{ID: "a2", Type: AgentTypeAnalyst, Task: "task 2"},
		{ID: "a3", Type: AgentTypeGeneric, Task: "task 3"},
	}

	start := time.Now()
	results, err := orch.Run(context.Background(), "chat1", specs, Budget{})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}

	// All should succeed.
	for i, r := range results {
		if r.Err != nil {
			t.Errorf("result[%d] error: %v", i, r.Err)
		}
		if r.Output == "" {
			t.Errorf("result[%d] has empty output", i)
		}
	}

	// Should run in parallel — total time should be roughly 1x (not 3x) the single agent time.
	if elapsed > 200*time.Millisecond {
		t.Errorf("took %v, expected parallel execution < 200ms", elapsed)
	}

	if callCount.Load() != 3 {
		t.Errorf("provider called %d times, want 3", callCount.Load())
	}
}

func TestOrchestratorMaxAgentsClamp(t *testing.T) {
	provider := &funcProvider{fn: func(_ context.Context, req llm.Request) (llm.Response, error) {
		return llm.Response{Content: "ok", Usage: llm.Usage{InputTokens: 10, OutputTokens: 5}}, nil
	}}

	orch := NewOrchestrator(provider, newTestRegistry(), nil, nil, zap.NewNop())
	specs := make([]AgentSpec, 10) // 10 agents
	for i := range specs {
		specs[i] = AgentSpec{ID: "a", Type: AgentTypeGeneric, Task: "task"}
	}

	results, err := orch.Run(context.Background(), "chat1", specs, Budget{MaxAgents: 3})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should be clamped to 3.
	if len(results) != 3 {
		t.Errorf("got %d results, want 3 (clamped)", len(results))
	}
}

func TestOrchestratorSharedBudget(t *testing.T) {
	callCount := atomic.Int32{}
	provider := &funcProvider{fn: func(_ context.Context, req llm.Request) (llm.Response, error) {
		callCount.Add(1)
		return llm.Response{
			Content: "expensive",
			Usage:   llm.Usage{InputTokens: 500, OutputTokens: 500},
		}, nil
	}}

	orch := NewOrchestrator(provider, newTestRegistry(), nil, nil, zap.NewNop())
	specs := []AgentSpec{
		{ID: "a1", Type: AgentTypeGeneric, Task: "task 1"},
		{ID: "a2", Type: AgentTypeGeneric, Task: "task 2"},
	}

	results, err := orch.Run(context.Background(), "chat1", specs, Budget{MaxTokens: 1500})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// At least one should have budget exhaustion.
	var budgetErrors int
	for _, r := range results {
		if errors.Is(r.Err, ErrBudgetExhausted) {
			budgetErrors++
		}
	}

	// With 1500 tokens and 1000 per call, the second agent should exhaust the budget.
	if budgetErrors == 0 {
		t.Error("expected at least one budget exhaustion error")
	}
}

func TestOrchestratorContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	provider := &funcProvider{fn: func(ctx context.Context, _ llm.Request) (llm.Response, error) {
		// Block until context is done.
		<-ctx.Done()
		return llm.Response{}, ctx.Err()
	}}

	orch := NewOrchestrator(provider, newTestRegistry(), nil, nil, zap.NewNop())
	specs := []AgentSpec{
		{ID: "a1", Type: AgentTypeGeneric, Task: "task"},
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	results, err := orch.Run(ctx, "chat1", specs, Budget{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("unexpected orchestrator error: %v", err)
	}

	// The agent should have a context error.
	if results[0].Err == nil {
		t.Error("expected context cancellation error")
	}
}

func TestOrchestratorAllAgentsFail(t *testing.T) {
	provider := &funcProvider{fn: func(_ context.Context, _ llm.Request) (llm.Response, error) {
		return llm.Response{}, errors.New("provider down")
	}}

	orch := NewOrchestrator(provider, newTestRegistry(), nil, nil, zap.NewNop())
	specs := []AgentSpec{
		{ID: "a1", Type: AgentTypeGeneric, Task: "task 1"},
		{ID: "a2", Type: AgentTypeGeneric, Task: "task 2"},
	}

	results, err := orch.Run(context.Background(), "chat1", specs, Budget{})
	if err != nil {
		t.Fatalf("orchestrator should not return error for individual failures: %v", err)
	}

	for _, r := range results {
		if r.Err == nil {
			t.Error("expected error for failed agent")
		}
	}
}

func TestOrchestratorEmptySpecs(t *testing.T) {
	orch := NewOrchestrator(nil, nil, nil, nil, zap.NewNop())
	results, err := orch.Run(context.Background(), "chat1", nil, Budget{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if results != nil {
		t.Errorf("expected nil results for empty specs, got %v", results)
	}
}

// funcProvider wraps a function as an llm.Provider.
type funcProvider struct {
	fn func(context.Context, llm.Request) (llm.Response, error)
}

func (p *funcProvider) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	return p.fn(ctx, req)
}

// newTestRegistry is defined in runner_test.go but we need it here too.
// Since both are in the same package, this is available.
func init() {
	// Ensure stubSkill satisfies skill.Skill at compile time.
	var _ skill.Skill = (*stubSkill)(nil)
}

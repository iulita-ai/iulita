package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/iulita-ai/iulita/internal/llm"
	"go.uber.org/zap"
)

type guardProvider func(context.Context, llm.Request) (llm.Response, error)

func (p guardProvider) Complete(ctx context.Context, r llm.Request) (llm.Response, error) {
	return p(ctx, r)
}

type guardSkill struct {
	name    string
	calls   int
	execute func(context.Context)
}

func (s *guardSkill) Name() string               { return s.name }
func (*guardSkill) Description() string          { return "guard fixture" }
func (*guardSkill) InputSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (s *guardSkill) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	s.calls++
	if s.execute != nil {
		s.execute(ctx)
	}
	return "late skill output", nil
}
func guardSpec() AgentSpec {
	return AgentSpec{ID: "guard", Type: AgentTypeGeneric, Task: "read", Tools: []string{"first", "second"}}
}
func guardCalls() llm.Response {
	return llm.Response{Content: "late private text", ToolCalls: []llm.ToolCall{{ID: "a", Name: "first", Input: json.RawMessage(`{}`)}, {ID: "b", Name: "second", Input: json.RawMessage(`{}`)}}}
}

func TestRunnerNeverExecutesLateToolsAfterProviderIgnoresCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := &guardSkill{name: "first"}
	p := guardProvider(func(context.Context, llm.Request) (llm.Response, error) { cancel(); return guardCalls(), nil })
	r := NewRunner(p, newTestRegistry(first), nil, nil, "topic", zap.NewNop())
	// No optional guard: cooperative cancellation alone must protect every runner.
	result := r.Run(ctx, guardSpec(), Budget{}, nil)
	if !errors.Is(result.Err, context.Canceled) || result.Output != "" || first.calls != 0 {
		t.Fatal(result, first.calls)
	}
}
func TestRunnerLiveGuardRejectsLateProviderAndKeepsOtherRunnerIndependent(t *testing.T) {
	unavailable := false
	denied := errors.New("topic deleted")
	first := &guardSkill{name: "first"}
	p := guardProvider(func(context.Context, llm.Request) (llm.Response, error) { unavailable = true; return guardCalls(), nil })
	r := NewRunner(p, newTestRegistry(first), nil, nil, "topic", zap.NewNop())
	r.SetExecutionGuard(func(context.Context) error {
		if unavailable {
			return denied
		}
		return nil
	})
	result := r.Run(context.Background(), guardSpec(), Budget{}, nil)
	if !errors.Is(result.Err, denied) || result.Output != "" || first.calls != 0 {
		t.Fatal(result, first.calls)
	}
	neighbor := NewRunner(guardProvider(func(context.Context, llm.Request) (llm.Response, error) {
		return llm.Response{Content: "independent"}, nil
	}), newTestRegistry(), nil, nil, "neighbor", zap.NewNop())
	if got := neighbor.Run(context.Background(), guardSpec(), Budget{}, nil); got.Err != nil || got.Output != "independent" {
		t.Fatal(got)
	}
}
func TestRunnerLiveGuardStopsBetweenToolsAndBeforeSynthesis(t *testing.T) {
	unavailable := false
	denied := errors.New("revoked")
	first := &guardSkill{name: "first", execute: func(context.Context) { unavailable = true }}
	second := &guardSkill{name: "second"}
	var providers atomic.Int64
	p := guardProvider(func(context.Context, llm.Request) (llm.Response, error) { providers.Add(1); return guardCalls(), nil })
	r := NewRunner(p, newTestRegistry(first, second), nil, nil, "topic", zap.NewNop())
	r.SetExecutionGuard(func(context.Context) error {
		if unavailable {
			return denied
		}
		return nil
	})
	result := r.Run(context.Background(), guardSpec(), Budget{}, nil)
	if !errors.Is(result.Err, denied) || result.Output != "" || first.calls != 1 || second.calls != 0 || providers.Load() != 1 {
		t.Fatal(result, first.calls, second.calls, providers.Load())
	}
}
func TestRunnerGuardReadErrorCannotFallBackToLateText(t *testing.T) {
	check := 0
	fault := errors.New("database failed")
	p := guardProvider(func(context.Context, llm.Request) (llm.Response, error) {
		return llm.Response{Content: "late text"}, nil
	})
	r := NewRunner(p, newTestRegistry(), nil, nil, "topic", zap.NewNop())
	// A transient guard failure is terminal for this run even if a later check
	// could succeed. Returning lastResp.Content would bypass the failed boundary.
	r.SetExecutionGuard(func(context.Context) error {
		check++
		if check == 2 {
			return fault
		}
		return nil
	})
	result := r.Run(context.Background(), guardSpec(), Budget{}, nil)
	if !errors.Is(result.Err, fault) || result.Output != "" {
		t.Fatal(result)
	}
}

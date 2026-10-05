package handlers

import (
	"context"
	"encoding/json"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/skill"
	"go.uber.org/zap"
	"testing"
)

type jobCaptureProvider struct{ requests []llm.Request }

func (p *jobCaptureProvider) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	p.requests = append(p.requests, req)
	return llm.Response{Content: "RUN"}, nil
}

func TestScheduledJobProfileForwardingAndWakeGateRole(t *testing.T) {
	store := memStore(t)
	job := &domain.AgentJob{Name: "job", Prompt: "read", ProfileID: "selected", Model: "legacy", WakeGatePrompt: "condition", DeliveryChatID: "chat", UserID: "actor", Enabled: true, Interval: "24h"}
	if err := store.CreateAgentJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	planner := AgentJobsJob(store, zap.NewNop())
	tasks := planner.CreateTasks(context.Background())
	if len(tasks) != 1 {
		t.Fatalf("tasks: %+v", tasks)
	}
	var payload agentJobPayload
	if err := json.Unmarshal([]byte(tasks[0].Payload), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ProfileID != "selected" || payload.UserID != "actor" || payload.Model != "legacy" || tasks[0].MaxAttempts != 1 {
		t.Fatalf("lost scheduler selection/identity: %+v %+v", payload, tasks[0])
	}
	provider := &jobCaptureProvider{}
	h := NewAgentJobHandler(store, provider, nil, nil, nil, zap.NewNop())
	if _, err := h.Handle(context.Background(), tasks[0].Payload); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("expected gate and run, got %d", len(provider.requests))
	}
	gate, run := provider.requests[0], provider.requests[1]
	if gate.ProfileID != "" || gate.Role != "background" || gate.RouteHint != llm.RouteHintCheap || gate.UserID != "actor" {
		t.Fatalf("gate inherited costly override: %+v", gate)
	}
	if run.ProfileID != "selected" || run.Role != "background" || run.ChatID != "chat" || run.UserID != "actor" || run.Operation != "job" {
		t.Fatalf("job request lost identity: %+v", run)
	}
	// The user-scoped path uses Runner and must keep the same typed reference
	// and role instead of accidentally reverting to its generic agent default.
	provider.requests = nil
	payload.WakeGatePrompt = ""
	encoded, _ := json.Marshal(payload)
	h = NewAgentJobHandler(store, provider, skill.NewRegistry(), nil, nil, zap.NewNop())
	if _, err := h.Handle(context.Background(), string(encoded)); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 || provider.requests[0].ProfileID != "selected" || provider.requests[0].Role != "background" || provider.requests[0].UserID != "actor" || provider.requests[0].ChatID != "chat" {
		t.Fatalf("agentic job lost selection: %+v", provider.requests)
	}
}

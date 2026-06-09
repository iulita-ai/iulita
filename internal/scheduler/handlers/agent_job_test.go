package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
)

// fixedProvider returns a preset response (and optional error) for every call.
type fixedProvider struct {
	content string
	err     error
	calls   int
}

func (p *fixedProvider) Complete(_ context.Context, _ llm.Request) (llm.Response, error) {
	p.calls++
	if p.err != nil {
		return llm.Response{}, p.err
	}
	return llm.Response{Content: p.content}, nil
}

type captureSender struct{ msgs []string }

func (s *captureSender) SendMessage(_ context.Context, _, text string) error {
	s.msgs = append(s.msgs, text)
	return nil
}

func memStore(t *testing.T) *sqlite.Store {
	t.Helper()
	store, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.RunMigrations(context.Background()); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	return store
}

func TestCronTimezone(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"0 9 * * 1-5", ""},
		{"CRON_TZ=Europe/Helsinki 0 9 * * 1-5", "Europe/Helsinki"},
		{"CRON_TZ=America/New_York 0 9 * * *", "America/New_York"},
		{"CRON_TZ=UTC", ""}, // no space → no field, treated as no tz
		{"", ""},
	}
	for _, tt := range tests {
		if got := cronTimezone(tt.in); got != tt.want {
			t.Errorf("cronTimezone(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func handle(t *testing.T, h *AgentJobHandler, p agentJobPayload) (string, error) {
	t.Helper()
	raw, _ := json.Marshal(p)
	return h.Handle(context.Background(), string(raw))
}

func TestLegacyJob_BarePathDelivers(t *testing.T) {
	prov := &fixedProvider{content: "the answer"}
	sender := &captureSender{}
	h := NewAgentJobHandler(memStore(t), prov, nil, nil, sender, zap.NewNop())

	out, err := handle(t, h, agentJobPayload{JobID: 1, JobName: "Daily", Prompt: "do it", DeliveryChatID: "chat1"}) // UserID "" → legacy
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if prov.calls != 1 {
		t.Errorf("legacy job should make exactly 1 LLM call, got %d", prov.calls)
	}
	if len(sender.msgs) != 1 || !strings.Contains(sender.msgs[0], "the answer") {
		t.Errorf("expected delivery containing the answer, got %v", sender.msgs)
	}
	if !strings.Contains(out, "completed") {
		t.Errorf("status = %s, want completed", out)
	}
}

func TestWakeGate_SkipsOnSentinel(t *testing.T) {
	prov := &fixedProvider{content: "SKIP"}
	sender := &captureSender{}
	h := NewAgentJobHandler(memStore(t), prov, nil, nil, sender, zap.NewNop())

	out, err := handle(t, h, agentJobPayload{JobID: 2, JobName: "Gated", Prompt: "do it", DeliveryChatID: "chat1", UserID: "u1", WakeGatePrompt: "only if something happened"})
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if !strings.Contains(out, "skipped_by_wake_gate") {
		t.Errorf("expected skip status, got %s", out)
	}
	if prov.calls != 1 {
		t.Errorf("only the gate call should happen, got %d", prov.calls)
	}
	if len(sender.msgs) != 0 {
		t.Errorf("skipped job must deliver nothing, got %v", sender.msgs)
	}
}

func TestWakeGate_FailsOpenOnError(t *testing.T) {
	// Gate errors → the handler should NOT skip (fail open). With a nil registry
	// the subsequent agentic run also calls the (erroring) provider and returns an
	// error, so Handle returns an error rather than silently dropping the job.
	prov := &fixedProvider{err: errors.New("boom")}
	h := NewAgentJobHandler(memStore(t), prov, nil, nil, &captureSender{}, zap.NewNop())

	out, err := handle(t, h, agentJobPayload{JobID: 3, JobName: "G", Prompt: "p", UserID: "u1", WakeGatePrompt: "cond"})
	if err == nil {
		t.Errorf("expected error to propagate (not a silent skip), got out=%s", out)
	}
	if strings.Contains(out, "skipped_by_wake_gate") {
		t.Error("gate error must NOT cause a skip")
	}
}

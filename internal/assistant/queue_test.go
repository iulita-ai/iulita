package assistant

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/storage"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
)

// mockProvider returns a fixed response for testing.
type mockProvider struct {
	response string
}

func (m *mockProvider) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	return llm.Response{
		Content: m.response + ": " + req.Message,
		Usage:   llm.Usage{InputTokens: 10, OutputTokens: 5},
	}, nil
}

// newTestStore creates an in-memory SQLite store for testing.
func newTestStore(t *testing.T) storage.Repository {
	t.Helper()
	store, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}
	if err := store.RunMigrations(context.Background()); err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func newTestAssistant(t *testing.T, provider llm.Provider) *Assistant {
	t.Helper()
	store := newTestStore(t)
	registry := skill.NewRegistry()
	return New(provider, store, registry, "test system prompt", "", 100000, zap.NewNop())
}

func TestSteer_NonBlocking(t *testing.T) {
	a := newTestAssistant(t, &mockProvider{response: "ok"})

	// Fill the steer channel.
	for i := range steerBufferSize {
		a.Steer(channel.IncomingMessage{ChatID: "test", Text: "msg" + string(rune('0'+i))}, nil)
	}

	// One more should not block (drops silently).
	done := make(chan struct{})
	go func() {
		a.Steer(channel.IncomingMessage{ChatID: "test", Text: "overflow"}, nil)
		close(done)
	}()

	select {
	case <-done:
		// Good — non-blocking.
	case <-time.After(1 * time.Second):
		t.Fatal("Steer blocked when queue was full")
	}
}

func TestFollowUp_NonBlocking(t *testing.T) {
	a := newTestAssistant(t, &mockProvider{response: "ok"})

	for i := range followUpBufferSize {
		a.FollowUp(channel.IncomingMessage{ChatID: "test", Text: "msg" + string(rune('0'+i))}, nil)
	}

	done := make(chan struct{})
	go func() {
		a.FollowUp(channel.IncomingMessage{ChatID: "test", Text: "overflow"}, nil)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("FollowUp blocked when queue was full")
	}
}

func TestRun_SteerBeforeFollowUp(t *testing.T) {
	var order []string
	var mu sync.Mutex

	a := newTestAssistant(t, &mockProvider{response: "ok"})

	// Pre-load both queues before starting Run.
	a.FollowUp(channel.IncomingMessage{ChatID: "test", Text: "followup1"}, func(resp string, err error) {
		mu.Lock()
		order = append(order, "followup1")
		mu.Unlock()
	})

	a.Steer(channel.IncomingMessage{ChatID: "test", Text: "steer1"}, func(resp string, err error) {
		mu.Lock()
		order = append(order, "steer1")
		mu.Unlock()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go a.Run(ctx)

	// Wait for both to be processed.
	deadline := time.After(3 * time.Second)
	for {
		mu.Lock()
		n := len(order)
		mu.Unlock()
		if n >= 2 {
			break
		}
		select {
		case <-deadline:
			mu.Lock()
			t.Fatalf("timed out waiting for messages, processed: %v", order)
			mu.Unlock()
		case <-time.After(50 * time.Millisecond):
		}
	}

	cancel()

	mu.Lock()
	defer mu.Unlock()

	if len(order) < 2 {
		t.Fatalf("expected 2 messages processed, got %d: %v", len(order), order)
	}
	if order[0] != "steer1" {
		t.Errorf("expected steer1 first, got order: %v", order)
	}
}

func TestRun_CallbackReceivesResponse(t *testing.T) {
	a := newTestAssistant(t, &mockProvider{response: "reply"})

	var gotResp string
	var gotErr error
	done := make(chan struct{})

	a.FollowUp(channel.IncomingMessage{ChatID: "test", Text: "hello"}, func(resp string, err error) {
		gotResp = resp
		gotErr = err
		close(done)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go a.Run(ctx)

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for callback")
	}

	cancel()

	if gotErr != nil {
		t.Fatalf("unexpected error: %v", gotErr)
	}
	if gotResp == "" {
		t.Fatal("expected non-empty response")
	}
}

func TestSessionStats_Atomic(t *testing.T) {
	a := newTestAssistant(t, &mockProvider{response: "ok"})

	_, err := a.HandleMessage(context.Background(), channel.IncomingMessage{
		ChatID: "test",
		Text:   "hello",
	})
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}

	input, output, requests := a.SessionStats()
	if input <= 0 {
		t.Errorf("expected positive input tokens, got %d", input)
	}
	if output <= 0 {
		t.Errorf("expected positive output tokens, got %d", output)
	}
	if requests != 1 {
		t.Errorf("expected 1 request, got %d", requests)
	}
}

func TestInjectedMessage_Types(t *testing.T) {
	m := InjectedMessage{
		IncomingMessage: channel.IncomingMessage{ChatID: "test", Text: "hello"},
		Priority:        PrioritySteer,
	}
	if m.Priority != PrioritySteer {
		t.Errorf("expected PrioritySteer, got %d", m.Priority)
	}

	m.Priority = PriorityFollowUp
	if m.Priority != PriorityFollowUp {
		t.Errorf("expected PriorityFollowUp, got %d", m.Priority)
	}
}

func TestRun_GracefulShutdown(t *testing.T) {
	a := newTestAssistant(t, &mockProvider{response: "ok"})

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		a.Run(ctx)
		close(done)
	}()

	// Give Run a moment to start, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// Good — Run exited.
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit after context cancellation")
	}
}

func TestRun_DrainSteerOnShutdown(t *testing.T) {
	a := newTestAssistant(t, &mockProvider{response: "ok"})

	var processed bool
	done := make(chan struct{})

	ctx, cancel := context.WithCancel(context.Background())

	// Add a steer message.
	a.Steer(channel.IncomingMessage{ChatID: "test", Text: "urgent"}, func(resp string, err error) {
		processed = true
		close(done)
	})

	// Start and immediately cancel — the steer message should still be drained.
	go a.Run(ctx)
	cancel()

	select {
	case <-done:
		if !processed {
			t.Fatal("steer message was not processed during drain")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for steer drain")
	}
}

// Verify the Assistant satisfies the Injector interface.
var _ Injector = (*Assistant)(nil)

// Suppress unused import warnings.
var _ domain.ChatMessage
var _ = sqlite.New

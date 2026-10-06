package assistant

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/storage"
)

// captureProvider records all LLM requests for assertion.
type captureProvider struct {
	requests []llm.Request
	response llm.Response
	err      error
}

func (p *captureProvider) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	p.requests = append(p.requests, req)
	return p.response, p.err
}

// mockCompressionStore implements just the storage methods used by compression.
type mockCompressionStore struct {
	storage.Repository
	deleteCalled bool
	savedMsg     *domain.ChatMessage
}

func (m *mockCompressionStore) DeleteMessagesBefore(_ context.Context, _ string, _ int64) error {
	m.deleteCalled = true
	return nil
}

func (m *mockCompressionStore) SaveMessage(_ context.Context, msg *domain.ChatMessage) error {
	m.savedMsg = msg
	return nil
}

func (m *mockCompressionStore) GetHistory(_ context.Context, _ string, _ int) ([]domain.ChatMessage, error) {
	// Return enough messages to trigger compression in CompressNow.
	now := time.Now()
	msgs := make([]domain.ChatMessage, 6)
	for i := range msgs {
		msgs[i] = domain.ChatMessage{
			ID:        int64(i + 1),
			ChatID:    "test-chat",
			Role:      domain.RoleUser,
			Content:   "message content",
			CreatedAt: now.Add(time.Duration(i) * time.Minute),
		}
	}
	return msgs, nil
}

func TestCompressIfNeeded_RouteHintCheap(t *testing.T) {
	provider := &captureProvider{
		response: llm.Response{Content: "Summary of conversation."},
	}

	a := &Assistant{
		provider:      provider,
		logger:        zap.NewNop(),
		contextWindow: 100000,
	}

	// Create a history of 6 messages.
	now := time.Now()
	history := make([]domain.ChatMessage, 6)
	for i := range history {
		history[i] = domain.ChatMessage{
			ID:        int64(i + 1),
			ChatID:    "test-chat",
			Role:      domain.RoleUser,
			Content:   "message content",
			CreatedAt: now.Add(time.Duration(i) * time.Minute),
		}
	}

	store := &mockCompressionStore{}
	a.store = store

	// Pass lastInputTokens above the 80% threshold to trigger compression.
	lastInputTokens := int64(float64(a.contextWindow)*compressionThreshold) + 1
	_, err := a.compressIfNeeded(context.Background(), "test-chat", history, lastInputTokens)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(provider.requests) == 0 {
		t.Fatal("expected at least one LLM request, got none")
	}

	for i, req := range provider.requests {
		if req.RouteHint != llm.RouteHintCheap {
			t.Errorf("request[%d]: expected RouteHint=%q, got %q", i, llm.RouteHintCheap, req.RouteHint)
		}
	}
}

func TestCompressIfNeeded_BelowThreshold_NoLLMCall(t *testing.T) {
	provider := &captureProvider{
		response: llm.Response{Content: "Should not be called."},
	}

	a := &Assistant{
		provider:      provider,
		logger:        zap.NewNop(),
		contextWindow: 100000,
	}

	history := make([]domain.ChatMessage, 6)
	for i := range history {
		history[i] = domain.ChatMessage{
			ID:      int64(i + 1),
			ChatID:  "test-chat",
			Role:    domain.RoleUser,
			Content: "message",
		}
	}

	// Below threshold — should not trigger compression.
	lastInputTokens := int64(float64(a.contextWindow)*compressionThreshold) - 1000
	_, err := a.compressIfNeeded(context.Background(), "test-chat", history, lastInputTokens)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(provider.requests) != 0 {
		t.Errorf("expected no LLM requests below threshold, got %d", len(provider.requests))
	}
}

func TestCompressNow_RouteHintCheap(t *testing.T) {
	provider := &captureProvider{
		response: llm.Response{Content: "Compressed summary."},
	}

	store := &mockCompressionStore{}

	a := &Assistant{
		provider:      provider,
		store:         store,
		logger:        zap.NewNop(),
		contextWindow: 100000,
	}

	_, err := a.CompressNow(context.Background(), "test-chat")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(provider.requests) == 0 {
		t.Fatal("expected at least one LLM request from CompressNow, got none")
	}

	for i, req := range provider.requests {
		if req.RouteHint != llm.RouteHintCheap {
			t.Errorf("CompressNow request[%d]: expected RouteHint=%q, got %q", i, llm.RouteHintCheap, req.RouteHint)
		}
	}
}

// failedCompressionStore models an atomic replacement that fails without
// committing changes. The SQLite transaction rollback is tested separately.
type failedCompressionStore struct {
	mockCompressionStore
	history          []domain.ChatMessage
	replacementError error
	replacementCalls int
}

func (s *failedCompressionStore) GetHistory(context.Context, string, int) ([]domain.ChatMessage, error) {
	return s.history, nil
}

func (s *failedCompressionStore) ReplaceMessagesWithSummary(context.Context, string, int64, *domain.ChatMessage) error {
	s.replacementCalls++
	return s.replacementError
}

func TestCompressionFailurePreservesHistoryAndError(t *testing.T) {
	for _, failure := range []string{"provider", "atomic replacement"} {
		for _, caller := range []string{"automatic", "forced", "manual"} {
			t.Run(failure+"/"+caller, func(t *testing.T) {
				ctx := context.Background()
				cause := errors.New("synthetic compression failure")
				history := make([]domain.ChatMessage, 6)
				for i := range history {
					history[i] = domain.ChatMessage{
						ID: int64(i + 1), ChatID: "test-chat", UserID: "owner",
						Role: domain.RoleUser, Content: "original message",
						CreatedAt: time.Unix(int64(i+1), 0).UTC(),
					}
				}
				original := append([]domain.ChatMessage(nil), history...)
				provider := &captureProvider{response: llm.Response{Content: "summary"}}
				store := &failedCompressionStore{history: history}
				wantReplacements := 0
				if failure == "provider" {
					provider.err = cause
				} else {
					store.replacementError = cause
					wantReplacements = 1
				}
				a := &Assistant{provider: provider, store: store, logger: zap.NewNop(), contextWindow: 100000}
				switch caller {
				case "automatic":
					returned, err := a.compressIfNeeded(ctx, "test-chat", history, 100000)
					if !errors.Is(err, cause) {
						t.Fatalf("compression error was lost: %v", err)
					}
					if !reflect.DeepEqual(returned, original) || &returned[0] != &history[0] {
						t.Fatal("failed compression did not return the original history")
					}
				case "forced":
					request := llm.Request{Message: "current message", History: history[:5], ForceTool: "remember", RequireToolOutcome: true}
					before := request
					returned := a.forceCompressRequest(ctx, "test-chat", &request, history)
					if !reflect.DeepEqual(returned, original) || &returned[0] != &history[0] || !reflect.DeepEqual(request, before) {
						t.Fatal("failed forced compression changed history or the pending request")
					}
				case "manual":
					count, err := a.CompressNow(ctx, "test-chat")
					if count != 0 || !errors.Is(err, cause) {
						t.Fatalf("manual compression reported success or lost its error: %d %v", count, err)
					}
				}
				if !reflect.DeepEqual(history, original) || !reflect.DeepEqual(store.history, original) {
					t.Fatal("compression failure mutated the original conversation")
				}
				if len(provider.requests) != 1 || store.replacementCalls != wantReplacements || store.deleteCalled || store.savedMsg != nil {
					t.Fatalf("unexpected retry or storage side effects: requests=%d replacements=%d deleted=%v saved=%v", len(provider.requests), store.replacementCalls, store.deleteCalled, store.savedMsg != nil)
				}
				req := provider.requests[0]
				if len(req.Tools) != 0 || len(req.ToolExchanges) != 0 || req.ForceTool != "" || req.RequireToolOutcome || req.Operation != "compression" {
					t.Fatal("summarization inherited tool execution from the conversation")
				}
			})
		}
	}
}

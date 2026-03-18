package assistant

import (
	"context"
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
}

func (p *captureProvider) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	p.requests = append(p.requests, req)
	return p.response, nil
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

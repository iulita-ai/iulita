package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/bookmark"
	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/storage"
)

// Compile-time interface checks.
var _ llm.Provider = (*captureProvider)(nil)
var _ llm.Provider = (*sequenceProvider)(nil)

// captureProvider records all LLM requests for RouteHint verification.
type captureProvider struct {
	requests []llm.Request
	response llm.Response
}

func (p *captureProvider) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	p.requests = append(p.requests, req)
	return p.response, nil
}

// mockSender satisfies channel.MessageSender.
type mockSender struct {
	messages []string
}

func (m *mockSender) SendMessage(_ context.Context, _, text string) error {
	m.messages = append(m.messages, text)
	return nil
}

// --- Mock stores for each handler ---

// mockHeartbeatStore provides data for heartbeat handler.
type mockHeartbeatStore struct {
	storage.Repository
	facts     []domain.Fact
	insights  []domain.Insight
	reminders []domain.Reminder
}

func (m *mockHeartbeatStore) GetRecentFacts(_ context.Context, _ string, _ int) ([]domain.Fact, error) {
	return m.facts, nil
}

func (m *mockHeartbeatStore) GetRecentInsights(_ context.Context, _ string, _ int) ([]domain.Insight, error) {
	return m.insights, nil
}

func (m *mockHeartbeatStore) ListReminders(_ context.Context, _ string) ([]domain.Reminder, error) {
	return m.reminders, nil
}

// mockTechFactStore provides data for techfact handler.
type mockTechFactStore struct {
	storage.Repository
	messages []domain.ChatMessage
	upserted int
}

func (m *mockTechFactStore) GetHistory(_ context.Context, _ string, _ int) ([]domain.ChatMessage, error) {
	return m.messages, nil
}

func (m *mockTechFactStore) UpsertTechFact(_ context.Context, _ *domain.TechFact) error {
	m.upserted++
	return nil
}

// mockInsightStore provides data for insight handler.
type mockInsightStore struct {
	storage.Repository
	facts     []domain.Fact
	insights  []domain.Insight
	techFacts []domain.TechFact
	saved     []domain.Insight
}

func (m *mockInsightStore) GetAllFacts(_ context.Context, _ string) ([]domain.Fact, error) {
	return m.facts, nil
}

func (m *mockInsightStore) GetAllFactsByUser(_ context.Context, _ string) ([]domain.Fact, error) {
	return m.facts, nil
}

func (m *mockInsightStore) GetRecentInsights(_ context.Context, _ string, _ int) ([]domain.Insight, error) {
	return m.insights, nil
}

func (m *mockInsightStore) GetRecentInsightsByUser(_ context.Context, _ string, _ int) ([]domain.Insight, error) {
	return m.insights, nil
}

func (m *mockInsightStore) GetTechFacts(_ context.Context, _ string) ([]domain.TechFact, error) {
	return m.techFacts, nil
}

func (m *mockInsightStore) GetTechFactsByUser(_ context.Context, _ string) ([]domain.TechFact, error) {
	return m.techFacts, nil
}

func (m *mockInsightStore) SaveInsight(_ context.Context, ins *domain.Insight) error {
	m.saved = append(m.saved, *ins)
	return nil
}

// mockRefineRouteStore provides data for bookmark refinement handler.
type mockRefineRouteStore struct {
	storage.Repository
	updatedFacts []int64
}

func (m *mockRefineRouteStore) UpdateFactContent(_ context.Context, id int64, _ string) error {
	m.updatedFacts = append(m.updatedFacts, id)
	return nil
}

// --- Tests ---

func assertAllRequestsCheap(t *testing.T, handlerName string, requests []llm.Request) {
	t.Helper()
	if len(requests) == 0 {
		t.Fatalf("%s: expected at least one LLM request, got none", handlerName)
	}
	for i := range requests {
		if requests[i].RouteHint != llm.RouteHintCheap {
			t.Errorf("%s: request[%d] RouteHint=%q, want %q", handlerName, i, requests[i].RouteHint, llm.RouteHintCheap)
		}
	}
}

func TestHeartbeatHandler_RouteHintCheap(t *testing.T) {
	provider := &captureProvider{
		response: llm.Response{Content: "HEARTBEAT_OK"},
	}
	store := &mockHeartbeatStore{
		facts: []domain.Fact{
			{ID: 1, ChatID: "chat1", Content: "User likes Go programming"},
		},
	}
	sender := &mockSender{}
	handler := NewHeartbeatHandler(store, provider, sender, zap.NewNop())

	payload, _ := json.Marshal(heartbeatPayload{ChatID: "chat1"})
	_, err := handler.Handle(context.Background(), string(payload))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertAllRequestsCheap(t, "HeartbeatHandler", provider.requests)
}

func TestTechFactAnalyzeHandler_RouteHintCheap(t *testing.T) {
	// Return valid JSON so the handler can parse it.
	provider := &captureProvider{
		response: llm.Response{Content: `[{"category":"topic","key":"topic:go","value":"high","confidence":0.9}]`},
	}

	// Need at least 10 messages total and 5 user messages.
	messages := make([]domain.ChatMessage, 12)
	for i := range messages {
		role := domain.RoleUser
		if i%2 == 0 {
			role = domain.RoleAssistant
		}
		messages[i] = domain.ChatMessage{
			ID:      int64(i + 1),
			ChatID:  "chat1",
			Role:    role,
			Content: fmt.Sprintf("message %d content for analysis", i),
		}
	}
	// Ensure enough user messages (need >= 5 user role).
	for i := 0; i < 6; i++ {
		messages[i].Role = domain.RoleUser
	}

	store := &mockTechFactStore{messages: messages}
	handler := NewTechFactAnalyzeHandler(store, provider, zap.NewNop())

	payload, _ := json.Marshal(techFactPayload{ChatID: "chat1"})
	_, err := handler.Handle(context.Background(), string(payload))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertAllRequestsCheap(t, "TechFactAnalyzeHandler", provider.requests)
}

func TestInsightGenerateHandler_RouteHintCheap(t *testing.T) {
	// The insight handler makes 2 LLM calls per pair: generateForPair + scoreInsight.
	// Use a sequencing provider that returns insight text first, then score.
	seqProvider := &sequenceProvider{
		responses: []llm.Response{
			{Content: "An interesting connection between clusters."},
			{Content: "4"},
		},
	}

	// Need at least minFacts (default 20) facts with enough variety for clustering.
	facts := make([]domain.Fact, 25)
	for i := range facts {
		facts[i] = domain.Fact{
			ID:        int64(i + 1),
			ChatID:    "chat1",
			Content:   fmt.Sprintf("fact about topic %d with some unique content %d", i%5, i),
			CreatedAt: time.Now(),
		}
	}

	store := &mockInsightStore{facts: facts}
	cfg := config.InsightsConfig{
		MinFacts: 20,
		MaxPairs: 1, // Limit to 1 pair for predictable test.
	}
	handler := NewInsightGenerateHandler(store, seqProvider, cfg, zap.NewNop())

	payload, _ := json.Marshal(insightPayload{ChatID: "chat1"})
	_, err := handler.Handle(context.Background(), string(payload))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertAllRequestsCheap(t, "InsightGenerateHandler", seqProvider.requests)
}

func TestRefineBookmarkHandler_RouteHintCheap(t *testing.T) {
	provider := &captureProvider{
		response: llm.Response{Content: "Key fact."},
	}
	store := &mockRefineRouteStore{}
	handler := NewRefineBookmarkHandler(store, provider, zap.NewNop())

	payload, _ := json.Marshal(bookmark.RefinePayload{
		FactID:  42,
		Content: "This is a very long response from the AI assistant that contains lots of detail and should be summarized into something much shorter by the LLM.",
	})

	_, err := handler.Handle(context.Background(), string(payload))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertAllRequestsCheap(t, "RefineBookmarkHandler", provider.requests)
}

// sequenceProvider returns responses in order, capturing all requests.
type sequenceProvider struct {
	requests  []llm.Request
	responses []llm.Response
	idx       int
}

func (p *sequenceProvider) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	p.requests = append(p.requests, req)
	if p.idx < len(p.responses) {
		resp := p.responses[p.idx]
		p.idx++
		return resp, nil
	}
	// Fallback for extra calls.
	return llm.Response{Content: "3"}, nil
}

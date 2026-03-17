package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/bookmark"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/storage"
)

type mockLLMProvider struct {
	response string
	err      error
}

func (m *mockLLMProvider) Complete(_ context.Context, _ llm.Request) (llm.Response, error) {
	if m.err != nil {
		return llm.Response{}, m.err
	}
	return llm.Response{Content: m.response}, nil
}

type mockRefineStore struct {
	storage.Repository
	updateFactContentCalls []struct {
		ID      int64
		Content string
	}
	updateErr error
}

func (m *mockRefineStore) UpdateFactContent(_ context.Context, id int64, content string) error {
	m.updateFactContentCalls = append(m.updateFactContentCalls, struct {
		ID      int64
		Content string
	}{id, content})
	return m.updateErr
}

func TestRefineBookmark_Success(t *testing.T) {
	store := &mockRefineStore{}
	provider := &mockLLMProvider{response: "Key fact extracted."}
	handler := NewRefineBookmarkHandler(store, provider, zap.NewNop())

	payload, _ := json.Marshal(bookmark.RefinePayload{
		FactID:  42,
		Content: "This is a very long response from the AI assistant that contains a lot of information about many topics and should be summarized into something shorter.",
	})

	result, err := handler.Handle(context.Background(), string(payload))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(store.updateFactContentCalls) != 1 {
		t.Fatalf("expected 1 UpdateFactContent call, got %d", len(store.updateFactContentCalls))
	}
	call := store.updateFactContentCalls[0]
	if call.ID != 42 {
		t.Errorf("expected fact_id=42, got %d", call.ID)
	}
	if call.Content != "Key fact extracted." {
		t.Errorf("expected refined content, got %q", call.Content)
	}

	var resultMap map[string]any
	json.Unmarshal([]byte(result), &resultMap)
	if resultMap["refined"] != true {
		t.Error("expected refined=true in result")
	}
}

func TestRefineBookmark_EmptyLLMResponse(t *testing.T) {
	store := &mockRefineStore{}
	provider := &mockLLMProvider{response: ""}
	handler := NewRefineBookmarkHandler(store, provider, zap.NewNop())

	payload, _ := json.Marshal(bookmark.RefinePayload{
		FactID:  1,
		Content: "Some content to refine",
	})

	result, err := handler.Handle(context.Background(), string(payload))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(store.updateFactContentCalls) != 0 {
		t.Error("UpdateFactContent should not be called when LLM returns empty")
	}

	var resultMap map[string]any
	json.Unmarshal([]byte(result), &resultMap)
	if resultMap["refined"] != false {
		t.Error("expected refined=false in result")
	}
}

func TestRefineBookmark_NoImprovement(t *testing.T) {
	original := "Short content"
	store := &mockRefineStore{}
	// Response is same length as original — no improvement.
	provider := &mockLLMProvider{response: "Short content!"}
	handler := NewRefineBookmarkHandler(store, provider, zap.NewNop())

	payload, _ := json.Marshal(bookmark.RefinePayload{
		FactID:  1,
		Content: original,
	})

	result, err := handler.Handle(context.Background(), string(payload))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(store.updateFactContentCalls) != 0 {
		t.Error("UpdateFactContent should not be called when refinement is not shorter")
	}

	var resultMap map[string]any
	json.Unmarshal([]byte(result), &resultMap)
	if resultMap["reason"] != "no_improvement" {
		t.Errorf("expected reason=no_improvement, got %v", resultMap["reason"])
	}
}

func TestRefineBookmark_LLMError(t *testing.T) {
	store := &mockRefineStore{}
	provider := &mockLLMProvider{err: errors.New("API error")}
	handler := NewRefineBookmarkHandler(store, provider, zap.NewNop())

	payload, _ := json.Marshal(bookmark.RefinePayload{
		FactID:  1,
		Content: "Content",
	})

	_, err := handler.Handle(context.Background(), string(payload))
	if err == nil {
		t.Fatal("expected error when LLM fails")
	}
}

func TestRefineBookmark_InvalidPayload(t *testing.T) {
	handler := NewRefineBookmarkHandler(nil, nil, zap.NewNop())

	_, err := handler.Handle(context.Background(), "invalid json")
	if err == nil {
		t.Fatal("expected error for invalid payload")
	}
}

func TestRefineBookmark_FactDeleted(t *testing.T) {
	store := &mockRefineStore{
		updateErr: errors.New("fact #99 not found"),
	}
	provider := &mockLLMProvider{response: "Short."}
	handler := NewRefineBookmarkHandler(store, provider, zap.NewNop())

	payload, _ := json.Marshal(bookmark.RefinePayload{
		FactID:  99,
		Content: "Very long content that should be refined into something shorter by the LLM provider",
	})

	result, err := handler.Handle(context.Background(), string(payload))
	if err != nil {
		t.Fatalf("deleted fact should be non-fatal, got: %v", err)
	}

	var resultMap map[string]any
	json.Unmarshal([]byte(result), &resultMap)
	if resultMap["reason"] != "deleted" {
		t.Errorf("expected reason=deleted, got %v", resultMap["reason"])
	}
}

func TestRefineBookmark_MissingFields(t *testing.T) {
	handler := NewRefineBookmarkHandler(nil, nil, zap.NewNop())

	payload, _ := json.Marshal(bookmark.RefinePayload{
		FactID:  0,
		Content: "",
	})

	_, err := handler.Handle(context.Background(), string(payload))
	if err == nil {
		t.Fatal("expected error for missing fact_id/content")
	}
}

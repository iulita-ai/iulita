package web

import (
	"context"
	"errors"
	"testing"
)

type mockSearcher struct {
	results []SearchResult
	err     error
}

func (m *mockSearcher) Search(_ context.Context, _ string, _ int) ([]SearchResult, error) {
	return m.results, m.err
}

func TestFallbackSearcher_FirstSuccess(t *testing.T) {
	primary := &mockSearcher{results: []SearchResult{{Title: "Primary", URL: "https://example.com"}}}
	fallback := &mockSearcher{results: []SearchResult{{Title: "Fallback", URL: "https://fallback.com"}}}

	fs := NewFallbackSearcher(primary, fallback)
	results, err := fs.Search(context.Background(), "test", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].Title != "Primary" {
		t.Errorf("expected Primary result, got %v", results)
	}
}

func TestFallbackSearcher_FallsThrough(t *testing.T) {
	primary := &mockSearcher{err: errors.New("rate limited")}
	fallback := &mockSearcher{results: []SearchResult{{Title: "Fallback", URL: "https://fallback.com"}}}

	fs := NewFallbackSearcher(primary, fallback)
	results, err := fs.Search(context.Background(), "test", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].Title != "Fallback" {
		t.Errorf("expected Fallback result, got %v", results)
	}
}

func TestFallbackSearcher_AllFail(t *testing.T) {
	p1 := &mockSearcher{err: errors.New("err1")}
	p2 := &mockSearcher{err: errors.New("err2")}

	fs := NewFallbackSearcher(p1, p2)
	_, err := fs.Search(context.Background(), "test", 5)
	if err == nil {
		t.Fatal("expected error when all providers fail")
	}
	if err.Error() != "err2" {
		t.Errorf("expected last error, got %v", err)
	}
}

func TestFallbackSearcher_EmptyResultsFallThrough(t *testing.T) {
	primary := &mockSearcher{results: nil} // no error but empty
	fallback := &mockSearcher{results: []SearchResult{{Title: "Got it"}}}

	fs := NewFallbackSearcher(primary, fallback)
	results, err := fs.Search(context.Background(), "test", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].Title != "Got it" {
		t.Errorf("expected fallback result, got %v", results)
	}
}

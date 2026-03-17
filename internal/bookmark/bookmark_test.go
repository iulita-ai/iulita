package bookmark

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage"
)

// mockStore implements the subset of storage.Repository used by bookmark.Service.
type mockStore struct {
	storage.Repository // embed to satisfy interface; unused methods panic

	saveFact              func(ctx context.Context, f *domain.Fact) error
	createTaskIfNotExists func(ctx context.Context, t *domain.Task) (bool, error)
	savedFacts            []*domain.Fact
	createdTasks          []*domain.Task
}

func (m *mockStore) SaveFact(ctx context.Context, f *domain.Fact) error {
	m.savedFacts = append(m.savedFacts, f)
	if m.saveFact != nil {
		return m.saveFact(ctx, f)
	}
	f.ID = int64(len(m.savedFacts)) // simulate autoincrement
	return nil
}

func (m *mockStore) CreateTaskIfNotExists(ctx context.Context, t *domain.Task) (bool, error) {
	m.createdTasks = append(m.createdTasks, t)
	if m.createTaskIfNotExists != nil {
		return m.createTaskIfNotExists(ctx, t)
	}
	return true, nil
}

func TestSave_Success(t *testing.T) {
	store := &mockStore{}
	svc := New(store, zap.NewNop())

	factID, err := svc.Save(context.Background(), "telegram:123", "user-uuid", "some content")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if factID == 0 {
		t.Fatal("expected non-zero fact ID")
	}

	// Verify fact was saved.
	if len(store.savedFacts) != 1 {
		t.Fatalf("expected 1 saved fact, got %d", len(store.savedFacts))
	}
	f := store.savedFacts[0]
	if f.SourceType != "bookmark" {
		t.Errorf("expected source_type=bookmark, got %q", f.SourceType)
	}
	if f.ChatID != "telegram:123" {
		t.Errorf("expected chat_id=telegram:123, got %q", f.ChatID)
	}
	if f.UserID != "user-uuid" {
		t.Errorf("expected user_id=user-uuid, got %q", f.UserID)
	}
	if f.Content != "some content" {
		t.Errorf("expected content='some content', got %q", f.Content)
	}
	if f.CreatedAt.IsZero() {
		t.Error("expected non-zero created_at")
	}

	// Verify refine task was enqueued.
	if len(store.createdTasks) != 1 {
		t.Fatalf("expected 1 created task, got %d", len(store.createdTasks))
	}
	task := store.createdTasks[0]
	if task.Type != TaskTypeRefineBookmark {
		t.Errorf("expected task type=%s, got %q", TaskTypeRefineBookmark, task.Type)
	}
	if task.Capabilities != "llm,storage" {
		t.Errorf("expected capabilities=llm,storage, got %q", task.Capabilities)
	}
	if !task.DeleteAfterRun {
		t.Error("expected delete_after_run=true")
	}
	if task.MaxAttempts != 2 {
		t.Errorf("expected max_attempts=2, got %d", task.MaxAttempts)
	}
}

func TestSave_EmptyContent(t *testing.T) {
	store := &mockStore{}
	svc := New(store, zap.NewNop())

	_, err := svc.Save(context.Background(), "chat1", "user1", "")
	if err == nil {
		t.Fatal("expected error for empty content")
	}
	if len(store.savedFacts) != 0 {
		t.Error("SaveFact should not have been called")
	}
}

func TestSave_StoreError(t *testing.T) {
	store := &mockStore{
		saveFact: func(ctx context.Context, f *domain.Fact) error {
			return errors.New("db error")
		},
	}
	svc := New(store, zap.NewNop())

	_, err := svc.Save(context.Background(), "chat1", "user1", "content")
	if err == nil {
		t.Fatal("expected error when SaveFact fails")
	}
	if len(store.createdTasks) != 0 {
		t.Error("CreateTask should not be called when SaveFact fails")
	}
}

func TestSave_TaskCreationError_NonFatal(t *testing.T) {
	store := &mockStore{
		createTaskIfNotExists: func(ctx context.Context, t *domain.Task) (bool, error) {
			return false, errors.New("task queue error")
		},
	}
	svc := New(store, zap.NewNop())

	factID, err := svc.Save(context.Background(), "chat1", "user1", "content")
	if err != nil {
		t.Fatalf("task creation error should be non-fatal, got: %v", err)
	}
	if factID == 0 {
		t.Fatal("expected non-zero fact ID even when task creation fails")
	}
	if len(store.savedFacts) != 1 {
		t.Error("fact should have been saved despite task error")
	}
}

func TestSave_LongContent_NotTruncated(t *testing.T) {
	store := &mockStore{}
	svc := New(store, zap.NewNop())

	// Content longer than 4000 chars should be saved in full.
	longContent := strings.Repeat("x", 10000)

	_, err := svc.Save(context.Background(), "chat1", "user1", longContent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.savedFacts[0].Content) != 10000 {
		t.Errorf("expected full content length 10000, got %d", len(store.savedFacts[0].Content))
	}
}

func TestSave_Timestamps(t *testing.T) {
	store := &mockStore{}
	svc := New(store, zap.NewNop())

	before := time.Now()
	_, err := svc.Save(context.Background(), "chat1", "user1", "content")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	f := store.savedFacts[0]
	if f.CreatedAt.Before(before) {
		t.Error("created_at should be >= test start time")
	}
	if f.LastAccessedAt.Before(before) {
		t.Error("last_accessed_at should be >= test start time")
	}
}

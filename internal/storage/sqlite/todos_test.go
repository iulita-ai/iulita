package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
)

func TestTodoItemLifecycle(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	userID := "user-1"
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, time.UTC)

	// Create a builtin task due today.
	item := &domain.TodoItem{
		UserID:   userID,
		Provider: "builtin",
		Title:    "Buy groceries",
		Notes:    "Milk, eggs, bread",
		DueDate:  &today,
		Priority: 2,
	}
	if err := store.CreateTodoItem(ctx, item); err != nil {
		t.Fatalf("CreateTodoItem: %v", err)
	}
	if item.ID == 0 {
		t.Fatal("expected non-zero ID after create")
	}

	// Get it back.
	got, err := store.GetTodoItem(ctx, item.ID, userID)
	if err != nil {
		t.Fatalf("GetTodoItem: %v", err)
	}
	if got.Title != "Buy groceries" {
		t.Errorf("expected 'Buy groceries', got %q", got.Title)
	}
	if got.Priority != 2 {
		t.Errorf("expected priority 2, got %d", got.Priority)
	}

	// List today.
	todayItems, err := store.ListTodoItemsDueToday(ctx, userID, now)
	if err != nil {
		t.Fatalf("ListTodoItemsDueToday: %v", err)
	}
	if len(todayItems) != 1 {
		t.Fatalf("expected 1 today item, got %d", len(todayItems))
	}

	// Complete it.
	if err := store.CompleteTodoItem(ctx, item.ID, userID); err != nil {
		t.Fatalf("CompleteTodoItem: %v", err)
	}

	// Today list should be empty now.
	todayItems, err = store.ListTodoItemsDueToday(ctx, userID, now)
	if err != nil {
		t.Fatalf("ListTodoItemsDueToday after complete: %v", err)
	}
	if len(todayItems) != 0 {
		t.Errorf("expected 0 today items after complete, got %d", len(todayItems))
	}

	// Reopen it.
	if err := store.ReopenTodoItem(ctx, item.ID, userID); err != nil {
		t.Fatalf("ReopenTodoItem: %v", err)
	}
	todayItems, err = store.ListTodoItemsDueToday(ctx, userID, now)
	if err != nil {
		t.Fatalf("ListTodoItemsDueToday after reopen: %v", err)
	}
	if len(todayItems) != 1 {
		t.Errorf("expected 1 today item after reopen, got %d", len(todayItems))
	}

	// Delete it.
	if err := store.DeleteTodoItem(ctx, item.ID, userID); err != nil {
		t.Fatalf("DeleteTodoItem: %v", err)
	}
	todayItems, err = store.ListTodoItemsDueToday(ctx, userID, now)
	if err != nil {
		t.Fatalf("ListTodoItemsDueToday after delete: %v", err)
	}
	if len(todayItems) != 0 {
		t.Errorf("expected 0 today items after delete, got %d", len(todayItems))
	}
}

func TestTodoItemOverdue(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	userID := "user-1"
	now := time.Now()

	// Create a task due yesterday.
	yesterday := time.Date(now.Year(), now.Month(), now.Day()-1, 12, 0, 0, 0, time.UTC)
	item := &domain.TodoItem{
		UserID:   userID,
		Provider: "builtin",
		Title:    "Overdue task",
		DueDate:  &yesterday,
	}
	if err := store.CreateTodoItem(ctx, item); err != nil {
		t.Fatalf("CreateTodoItem: %v", err)
	}

	overdue, err := store.ListTodoItemsOverdue(ctx, userID, now)
	if err != nil {
		t.Fatalf("ListTodoItemsOverdue: %v", err)
	}
	if len(overdue) != 1 {
		t.Fatalf("expected 1 overdue item, got %d", len(overdue))
	}
	if overdue[0].Title != "Overdue task" {
		t.Errorf("expected 'Overdue task', got %q", overdue[0].Title)
	}

	// Should NOT appear in today's list.
	todayItems, err := store.ListTodoItemsDueToday(ctx, userID, now)
	if err != nil {
		t.Fatalf("ListTodoItemsDueToday: %v", err)
	}
	if len(todayItems) != 0 {
		t.Errorf("expected 0 today items for overdue task, got %d", len(todayItems))
	}
}

func TestTodoItemUpcoming(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	userID := "user-1"
	now := time.Now()

	// Create tasks at various dates.
	for i, title := range []string{"Day1", "Day3", "Day5", "Day10"} {
		d := time.Date(now.Year(), now.Month(), now.Day()+[]int{0, 2, 4, 9}[i], 12, 0, 0, 0, time.UTC)
		item := &domain.TodoItem{
			UserID:   userID,
			Provider: "builtin",
			Title:    title,
			DueDate:  &d,
		}
		if err := store.CreateTodoItem(ctx, item); err != nil {
			t.Fatalf("CreateTodoItem %s: %v", title, err)
		}
	}

	// 7-day window should get Day1, Day3, Day5 but not Day10.
	upcoming, err := store.ListTodoItemsUpcoming(ctx, userID, now, 7)
	if err != nil {
		t.Fatalf("ListTodoItemsUpcoming: %v", err)
	}
	if len(upcoming) != 3 {
		t.Fatalf("expected 3 upcoming items (7 days), got %d", len(upcoming))
	}
}

func TestTodoItemUpsertExternal(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	userID := "user-1"

	dueDate := time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC)
	item := &domain.TodoItem{
		UserID:     userID,
		Provider:   "todoist",
		ExternalID: "ext-123",
		Title:      "Original title",
		DueDate:    &dueDate,
	}
	if err := store.UpsertTodoItemByExternal(ctx, item); err != nil {
		t.Fatalf("UpsertTodoItemByExternal (insert): %v", err)
	}
	firstID := item.ID

	// Upsert again with updated title.
	item2 := &domain.TodoItem{
		UserID:     userID,
		Provider:   "todoist",
		ExternalID: "ext-123",
		Title:      "Updated title",
		DueDate:    &dueDate,
	}
	if err := store.UpsertTodoItemByExternal(ctx, item2); err != nil {
		t.Fatalf("UpsertTodoItemByExternal (update): %v", err)
	}

	// Should reuse the same row.
	if item2.ID != firstID {
		t.Errorf("expected same ID %d on upsert, got %d", firstID, item2.ID)
	}

	// Verify updated title.
	got, err := store.GetTodoItem(ctx, firstID, userID)
	if err != nil {
		t.Fatalf("GetTodoItem: %v", err)
	}
	if got.Title != "Updated title" {
		t.Errorf("expected 'Updated title', got %q", got.Title)
	}

	// Delete stale items (keep ext-123).
	deleted, err := store.DeleteSyncedTodoItemsNotIn(ctx, userID, "todoist", []string{"ext-123"})
	if err != nil {
		t.Fatalf("DeleteSyncedTodoItemsNotIn: %v", err)
	}
	if deleted != 0 {
		t.Errorf("expected 0 deleted (keeping ext-123), got %d", deleted)
	}

	// Delete stale items (keep nothing).
	deleted, err = store.DeleteSyncedTodoItemsNotIn(ctx, userID, "todoist", []string{})
	if err != nil {
		t.Fatalf("DeleteSyncedTodoItemsNotIn: %v", err)
	}
	if deleted != 1 {
		t.Errorf("expected 1 deleted (keeping nothing), got %d", deleted)
	}
}

func TestTodoItemUserScoping(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, time.UTC)

	// Create tasks for two different users.
	for _, uid := range []string{"user-1", "user-2"} {
		item := &domain.TodoItem{
			UserID:   uid,
			Provider: "builtin",
			Title:    "Task for " + uid,
			DueDate:  &today,
		}
		if err := store.CreateTodoItem(ctx, item); err != nil {
			t.Fatalf("CreateTodoItem: %v", err)
		}
	}

	// user-1 should see only their task.
	items, err := store.ListTodoItemsDueToday(ctx, "user-1", now)
	if err != nil {
		t.Fatalf("ListTodoItemsDueToday: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item for user-1, got %d", len(items))
	}
	if items[0].Title != "Task for user-1" {
		t.Errorf("expected 'Task for user-1', got %q", items[0].Title)
	}

	// user-1 should not be able to get user-2's task.
	_, err = store.GetTodoItem(ctx, 2, "user-1")
	if err == nil {
		t.Error("expected error getting other user's task")
	}
}

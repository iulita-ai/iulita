package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/uptrace/bun"
)

// CreateTodoItem inserts a new todo item.
func (s *Store) CreateTodoItem(ctx context.Context, item *domain.TodoItem) error {
	item.CreatedAt = time.Now()
	item.UpdatedAt = item.CreatedAt
	_, err := s.db.NewInsert().Model(item).Exec(ctx)
	if err != nil {
		return fmt.Errorf("inserting todo item: %w", err)
	}
	return nil
}

// GetTodoItem returns a single todo item by ID, scoped to user.
func (s *Store) GetTodoItem(ctx context.Context, id int64, userID string) (*domain.TodoItem, error) {
	var item domain.TodoItem
	err := s.db.NewSelect().
		Model(&item).
		Where("id = ?", id).
		Where("user_id = ?", userID).
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting todo item %d: %w", id, err)
	}
	return &item, nil
}

// UpdateTodoItem updates a todo item.
func (s *Store) UpdateTodoItem(ctx context.Context, item *domain.TodoItem) error {
	item.UpdatedAt = time.Now()
	_, err := s.db.NewUpdate().
		Model(item).
		WherePK().
		Where("user_id = ?", item.UserID).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("updating todo item %d: %w", item.ID, err)
	}
	return nil
}

// DeleteTodoItem deletes a todo item by ID, scoped to user.
func (s *Store) DeleteTodoItem(ctx context.Context, id int64, userID string) error {
	_, err := s.db.NewDelete().
		Model((*domain.TodoItem)(nil)).
		Where("id = ?", id).
		Where("user_id = ?", userID).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("deleting todo item %d: %w", id, err)
	}
	return nil
}

// CompleteTodoItem marks a todo item as completed.
func (s *Store) CompleteTodoItem(ctx context.Context, id int64, userID string) error {
	now := time.Now()
	_, err := s.db.NewUpdate().
		Model((*domain.TodoItem)(nil)).
		Set("completed_at = ?", now).
		Set("updated_at = ?", now).
		Where("id = ?", id).
		Where("user_id = ?", userID).
		Where("completed_at IS NULL").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("completing todo item %d: %w", id, err)
	}
	return nil
}

// ReopenTodoItem clears completed_at on a todo item.
func (s *Store) ReopenTodoItem(ctx context.Context, id int64, userID string) error {
	now := time.Now()
	_, err := s.db.NewUpdate().
		Model((*domain.TodoItem)(nil)).
		Set("completed_at = NULL").
		Set("updated_at = ?", now).
		Where("id = ?", id).
		Where("user_id = ?", userID).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("reopening todo item %d: %w", id, err)
	}
	return nil
}

// ListTodoItemsDueToday returns incomplete items due today for a user.
func (s *Store) ListTodoItemsDueToday(ctx context.Context, userID string, now time.Time) ([]domain.TodoItem, error) {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	tomorrow := today.Add(24 * time.Hour)

	var items []domain.TodoItem
	err := s.db.NewSelect().
		Model(&items).
		Where("user_id = ?", userID).
		Where("completed_at IS NULL").
		Where("due_date >= ?", today).
		Where("due_date < ?", tomorrow).
		Order("priority DESC", "due_date ASC", "title ASC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing today's todos: %w", err)
	}
	return items, nil
}

// ListTodoItemsOverdue returns incomplete items overdue for a user.
func (s *Store) ListTodoItemsOverdue(ctx context.Context, userID string, now time.Time) ([]domain.TodoItem, error) {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	var items []domain.TodoItem
	err := s.db.NewSelect().
		Model(&items).
		Where("user_id = ?", userID).
		Where("completed_at IS NULL").
		Where("due_date < ?", today).
		Order("due_date ASC", "priority DESC", "title ASC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing overdue todos: %w", err)
	}
	return items, nil
}

// ListTodoItemsUpcoming returns incomplete items due within the next N days for a user.
func (s *Store) ListTodoItemsUpcoming(ctx context.Context, userID string, now time.Time, days int) ([]domain.TodoItem, error) {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	end := today.Add(time.Duration(days) * 24 * time.Hour)

	var items []domain.TodoItem
	err := s.db.NewSelect().
		Model(&items).
		Where("user_id = ?", userID).
		Where("completed_at IS NULL").
		Where("due_date >= ?", today).
		Where("due_date < ?", end).
		Order("due_date ASC", "priority DESC", "title ASC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing upcoming todos: %w", err)
	}
	return items, nil
}

// ListTodoItemsAll returns all incomplete items for a user (no date filter).
func (s *Store) ListTodoItemsAll(ctx context.Context, userID string, limit int) ([]domain.TodoItem, error) {
	var items []domain.TodoItem
	q := s.db.NewSelect().
		Model(&items).
		Where("user_id = ?", userID).
		Where("completed_at IS NULL").
		Order("due_date ASC NULLS LAST", "priority DESC", "title ASC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Scan(ctx); err != nil {
		return nil, fmt.Errorf("listing all todos: %w", err)
	}
	return items, nil
}

// UpsertTodoItemByExternal creates or updates a todo item matched by (user_id, provider, external_id).
// Uses INSERT ON CONFLICT to avoid race conditions during concurrent syncs.
func (s *Store) UpsertTodoItemByExternal(ctx context.Context, item *domain.TodoItem) error {
	item.UpdatedAt = time.Now()
	if item.CreatedAt.IsZero() {
		item.CreatedAt = item.UpdatedAt
	}

	_, err := s.db.NewInsert().
		Model(item).
		On("CONFLICT (user_id, provider, external_id) WHERE external_id != '' DO UPDATE").
		Set("title = EXCLUDED.title").
		Set("notes = EXCLUDED.notes").
		Set("due_date = EXCLUDED.due_date").
		Set("completed_at = EXCLUDED.completed_at").
		Set("priority = EXCLUDED.priority").
		Set("labels = EXCLUDED.labels").
		Set("project_name = EXCLUDED.project_name").
		Set("url = EXCLUDED.url").
		Set("synced_at = EXCLUDED.synced_at").
		Set("updated_at = EXCLUDED.updated_at").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("upserting synced todo: %w", err)
	}
	return nil
}

// DeleteSyncedTodoItemsNotIn removes synced items for a provider/user that are not in the given external IDs set.
func (s *Store) DeleteSyncedTodoItemsNotIn(ctx context.Context, userID, provider string, keepExternalIDs []string) (int, error) {
	if len(keepExternalIDs) == 0 {
		// Delete all synced items from this provider for user.
		res, err := s.db.NewDelete().
			Model((*domain.TodoItem)(nil)).
			Where("user_id = ?", userID).
			Where("provider = ?", provider).
			Exec(ctx)
		if err != nil {
			return 0, fmt.Errorf("deleting synced todos: %w", err)
		}
		n, _ := res.RowsAffected()
		return int(n), nil
	}

	res, err := s.db.NewDelete().
		Model((*domain.TodoItem)(nil)).
		Where("user_id = ?", userID).
		Where("provider = ?", provider).
		Where("external_id NOT IN (?)", bun.List(keepExternalIDs)).
		Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("deleting stale synced todos: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// CountTodoItemsByProvider returns counts of incomplete items per provider for a user.
func (s *Store) CountTodoItemsByProvider(ctx context.Context, userID string) (map[string]int, error) {
	type result struct {
		Provider string `bun:"provider"`
		Count    int    `bun:"count"`
	}
	var results []result
	err := s.db.NewSelect().
		Model((*domain.TodoItem)(nil)).
		ColumnExpr("provider, COUNT(*) as count").
		Where("user_id = ?", userID).
		Where("completed_at IS NULL").
		Group("provider").
		Scan(ctx, &results)
	if err != nil {
		return nil, fmt.Errorf("counting todos by provider: %w", err)
	}
	counts := make(map[string]int, len(results))
	for _, r := range results {
		counts[r.Provider] = r.Count
	}
	return counts, nil
}

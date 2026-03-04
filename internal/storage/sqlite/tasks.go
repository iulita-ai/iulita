package sqlite

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage"
)

func (s *Store) CreateTask(ctx context.Context, t *domain.Task) error {
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now()
	}
	if t.ScheduledAt.IsZero() {
		t.ScheduledAt = time.Now()
	}
	if t.Status == "" {
		t.Status = domain.TaskStatusPending
	}
	if t.MaxAttempts == 0 {
		t.MaxAttempts = 3
	}
	_, err := s.db.NewInsert().Model(t).Exec(ctx)
	if err != nil {
		return fmt.Errorf("inserting task: %w", err)
	}
	return nil
}

func (s *Store) CreateTaskIfNotExists(ctx context.Context, t *domain.Task) (bool, error) {
	if t.UniqueKey == "" {
		if err := s.CreateTask(ctx, t); err != nil {
			return false, err
		}
		return true, nil
	}

	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now()
	}
	if t.ScheduledAt.IsZero() {
		t.ScheduledAt = time.Now()
	}
	if t.Status == "" {
		t.Status = domain.TaskStatusPending
	}
	if t.MaxAttempts == 0 {
		t.MaxAttempts = 3
	}

	// Check if a pending/claimed/running task with this key already exists.
	exists, err := s.db.NewSelect().
		Model((*domain.Task)(nil)).
		Where("unique_key = ?", t.UniqueKey).
		Where("status IN (?)", domain.TaskStatusPending, domain.TaskStatusClaimed, domain.TaskStatusRunning).
		Exists(ctx)
	if err != nil {
		return false, fmt.Errorf("checking task existence: %w", err)
	}
	if exists {
		return false, nil
	}

	_, err = s.db.NewInsert().Model(t).Exec(ctx)
	if err != nil {
		// Unique constraint violation is expected in race conditions.
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			return false, nil
		}
		return false, fmt.Errorf("inserting task: %w", err)
	}
	return true, nil
}

func (s *Store) ClaimTask(ctx context.Context, workerID string, capabilities []string) (*domain.Task, error) {
	now := time.Now()

	// Load candidate tasks that are pending and due.
	var candidates []domain.Task
	err := s.db.NewSelect().
		Model(&candidates).
		Where("status = ?", domain.TaskStatusPending).
		Where("scheduled_at <= ?", now).
		OrderExpr("priority DESC, scheduled_at ASC").
		Limit(20).
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("querying pending tasks: %w", err)
	}

	// Find first task whose required capabilities are satisfied.
	capSet := make(map[string]struct{}, len(capabilities))
	for _, c := range capabilities {
		capSet[c] = struct{}{}
	}

	for _, t := range candidates {
		if !capsMatch(t.Capabilities, capSet) {
			continue
		}

		// Atomically claim: only succeeds if still pending.
		res, err := s.db.NewUpdate().
			Model((*domain.Task)(nil)).
			Set("status = ?", domain.TaskStatusClaimed).
			Set("worker_id = ?", workerID).
			Set("claimed_at = ?", now).
			Where("id = ? AND status = ?", t.ID, domain.TaskStatusPending).
			Exec(ctx)
		if err != nil {
			return nil, fmt.Errorf("claiming task %d: %w", t.ID, err)
		}
		rows, _ := res.RowsAffected()
		if rows == 0 {
			continue // someone else claimed it
		}

		t.Status = domain.TaskStatusClaimed
		t.WorkerID = workerID
		t.ClaimedAt = &now
		return &t, nil
	}

	return nil, nil // no matching task available
}

func (s *Store) StartTask(ctx context.Context, taskID int64, workerID string) error {
	now := time.Now()
	res, err := s.db.NewUpdate().
		Model((*domain.Task)(nil)).
		Set("status = ?", domain.TaskStatusRunning).
		Set("started_at = ?", now).
		Set("attempts = attempts + 1").
		Where("id = ? AND worker_id = ? AND status = ?", taskID, workerID, domain.TaskStatusClaimed).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("starting task %d: %w", taskID, err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("task %d not claimable (wrong state or worker)", taskID)
	}
	return nil
}

func (s *Store) CompleteTask(ctx context.Context, taskID int64, result string) error {
	// Check if task should be deleted after completion.
	var t domain.Task
	err := s.db.NewSelect().
		Model(&t).
		Column("delete_after_run").
		Where("id = ? AND status = ?", taskID, domain.TaskStatusRunning).
		Scan(ctx)
	if err != nil {
		return fmt.Errorf("loading task %d for completion: %w", taskID, err)
	}

	if t.DeleteAfterRun {
		_, err = s.db.NewDelete().
			Model((*domain.Task)(nil)).
			Where("id = ? AND status = ?", taskID, domain.TaskStatusRunning).
			Exec(ctx)
		if err != nil {
			return fmt.Errorf("deleting task %d after run: %w", taskID, err)
		}
		return nil
	}

	now := time.Now()
	_, err = s.db.NewUpdate().
		Model((*domain.Task)(nil)).
		Set("status = ?", domain.TaskStatusDone).
		Set("finished_at = ?", now).
		Set("result = ?", result).
		Set("unique_key = ''"). // free the key so the job can be re-created
		Where("id = ? AND status = ?", taskID, domain.TaskStatusRunning).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("completing task %d: %w", taskID, err)
	}
	return nil
}

func (s *Store) FailTask(ctx context.Context, taskID int64, errMsg string) error {
	now := time.Now()

	// Load the task to check retry eligibility.
	var t domain.Task
	err := s.db.NewSelect().
		Model(&t).
		Where("id = ?", taskID).
		Scan(ctx)
	if err != nil {
		return fmt.Errorf("loading task %d: %w", taskID, err)
	}

	newStatus := domain.TaskStatusFailed
	if t.Attempts < t.MaxAttempts {
		// Re-queue for retry with a backoff delay.
		newStatus = domain.TaskStatusPending
		backoff := time.Duration(t.Attempts) * 30 * time.Second
		retryAt := now.Add(backoff)
		_, err = s.db.NewUpdate().
			Model((*domain.Task)(nil)).
			Set("status = ?", newStatus).
			Set("error = ?", errMsg).
			Set("worker_id = ''").
			Set("claimed_at = NULL").
			Set("started_at = NULL").
			Set("scheduled_at = ?", retryAt).
			Where("id = ?", taskID).
			Exec(ctx)
	} else {
		_, err = s.db.NewUpdate().
			Model((*domain.Task)(nil)).
			Set("status = ?", newStatus).
			Set("finished_at = ?", now).
			Set("error = ?", errMsg).
			Set("unique_key = ''"). // free the key so the job can be re-created
			Where("id = ?", taskID).
			Exec(ctx)
	}

	if err != nil {
		return fmt.Errorf("failing task %d: %w", taskID, err)
	}
	return nil
}

func (s *Store) ListTasks(ctx context.Context, filter storage.TaskFilter) ([]domain.Task, error) {
	var tasks []domain.Task
	q := s.db.NewSelect().Model(&tasks)
	if filter.Status != nil {
		q = q.Where("status = ?", *filter.Status)
	}
	if filter.Type != "" {
		q = q.Where("type = ?", filter.Type)
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	q = q.OrderExpr("created_at DESC").Limit(limit)
	err := q.Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing tasks: %w", err)
	}
	return tasks, nil
}

func (s *Store) CountTasksByStatus(ctx context.Context) (map[domain.TaskStatus]int, error) {
	type row struct {
		Status domain.TaskStatus `bun:"status"`
		Count  int               `bun:"count"`
	}
	var rows []row
	err := s.db.NewSelect().
		Model((*domain.Task)(nil)).
		ColumnExpr("status, COUNT(*) as count").
		GroupExpr("status").
		Scan(ctx, &rows)
	if err != nil {
		return nil, fmt.Errorf("counting tasks: %w", err)
	}
	result := make(map[domain.TaskStatus]int)
	for _, r := range rows {
		result[r.Status] = r.Count
	}
	return result, nil
}

func (s *Store) CleanupStaleTasks(ctx context.Context, timeout time.Duration) (int, error) {
	cutoff := time.Now().Add(-timeout)
	res, err := s.db.NewUpdate().
		Model((*domain.Task)(nil)).
		Set("status = ?", domain.TaskStatusPending).
		Set("worker_id = ''").
		Set("claimed_at = NULL").
		Set("started_at = NULL").
		Where("status IN (?, ?) AND claimed_at < ?",
			domain.TaskStatusClaimed, domain.TaskStatusRunning, cutoff).
		Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("cleaning stale tasks: %w", err)
	}
	rows, _ := res.RowsAffected()
	return int(rows), nil
}

func (s *Store) DeleteOldTasks(ctx context.Context, olderThan time.Duration) (int, error) {
	cutoff := time.Now().Add(-olderThan)
	res, err := s.db.NewDelete().
		Model((*domain.Task)(nil)).
		Where("status IN (?, ?) AND finished_at < ?",
			domain.TaskStatusDone, domain.TaskStatusFailed, cutoff).
		Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("deleting old tasks: %w", err)
	}
	rows, _ := res.RowsAffected()
	return int(rows), nil
}

func (s *Store) GetSchedulerState(ctx context.Context, name string) (*domain.SchedulerState, error) {
	var state domain.SchedulerState
	err := s.db.NewSelect().
		Model(&state).
		Where("name = ?", name).
		Scan(ctx)
	if err != nil {
		return nil, err
	}
	return &state, nil
}

func (s *Store) SaveSchedulerState(ctx context.Context, state *domain.SchedulerState) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO scheduler_states (name, last_run, next_run, enabled)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			last_run = excluded.last_run,
			next_run = excluded.next_run,
			enabled = excluded.enabled
	`, state.Name, state.LastRun, state.NextRun, state.Enabled)
	if err != nil {
		return fmt.Errorf("saving scheduler state: %w", err)
	}
	return nil
}

// capsMatch checks if a task's required capabilities are a subset of the worker's capabilities.
func capsMatch(required string, workerCaps map[string]struct{}) bool {
	if required == "" {
		return true
	}
	for _, cap := range strings.Split(required, ",") {
		cap = strings.TrimSpace(cap)
		if cap == "" {
			continue
		}
		if _, ok := workerCaps[cap]; !ok {
			return false
		}
	}
	return true
}

package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
)

func (s *Store) CreateReminder(ctx context.Context, r *domain.Reminder) error {
	_, err := s.db.NewInsert().Model(r).Exec(ctx)
	if err != nil {
		return fmt.Errorf("inserting reminder: %w", err)
	}
	return nil
}

func (s *Store) ListReminders(ctx context.Context, chatID string) ([]domain.Reminder, error) {
	var reminders []domain.Reminder
	err := s.db.NewSelect().
		Model(&reminders).
		Where("chat_id = ? AND status = ?", chatID, "pending").
		Order("due_at ASC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing reminders: %w", err)
	}
	return reminders, nil
}

func (s *Store) DeleteReminder(ctx context.Context, id int64, chatID string) error {
	res, err := s.db.NewUpdate().
		Model((*domain.Reminder)(nil)).
		Set("status = ?", "cancelled").
		Where("id = ? AND chat_id = ? AND status = ?", id, chatID, "pending").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("deleting reminder: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("reminder #%d not found or already fired", id)
	}
	return nil
}

func (s *Store) GetDueReminders(ctx context.Context, now time.Time) ([]domain.Reminder, error) {
	var reminders []domain.Reminder
	err := s.db.NewSelect().
		Model(&reminders).
		Where("status = ? AND due_at <= ?", "pending", now).
		Order("due_at ASC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("querying due reminders: %w", err)
	}
	return reminders, nil
}

func (s *Store) MarkReminderFired(ctx context.Context, id int64) error {
	_, err := s.db.NewUpdate().
		Model((*domain.Reminder)(nil)).
		Set("status = ?", "fired").
		Where("id = ?", id).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("marking reminder fired: %w", err)
	}
	return nil
}

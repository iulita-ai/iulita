package sqlite

import (
	"context"
	"fmt"

	"github.com/iulita-ai/iulita/internal/domain"
)

func (s *Store) GetChatIDs(ctx context.Context) ([]string, error) {
	var chatIDs []string
	err := s.db.NewSelect().
		Model((*domain.ChatMessage)(nil)).
		ColumnExpr("DISTINCT chat_id").
		Order("chat_id ASC").
		Scan(ctx, &chatIDs)
	if err != nil {
		return nil, fmt.Errorf("getting chat IDs: %w", err)
	}
	return chatIDs, nil
}

func (s *Store) CountMessages(ctx context.Context, chatID string) (int, error) {
	count, err := s.db.NewSelect().
		Model((*domain.ChatMessage)(nil)).
		Where("chat_id = ?", chatID).
		Count(ctx)
	if err != nil {
		return 0, fmt.Errorf("counting messages: %w", err)
	}
	return count, nil
}

func (s *Store) ListAllReminders(ctx context.Context, chatID string) ([]domain.Reminder, error) {
	var reminders []domain.Reminder
	q := s.db.NewSelect().
		Model(&reminders).
		Order("due_at DESC")
	if chatID != "" {
		q = q.Where("chat_id = ?", chatID)
	}
	err := q.Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing all reminders: %w", err)
	}
	return reminders, nil
}

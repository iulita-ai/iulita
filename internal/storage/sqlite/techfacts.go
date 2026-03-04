package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
)

func (s *Store) UpsertTechFact(ctx context.Context, f *domain.TechFact) error {
	now := time.Now()
	// Running average for confidence: new_avg = (old_avg * count + new_value) / (count + 1)
	// Value is recalculated from the averaged confidence as a percentage.
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tech_facts (chat_id, user_id, category, key, value, confidence, update_count, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)
		ON CONFLICT(chat_id, category, key) DO UPDATE SET
			confidence = (tech_facts.confidence * tech_facts.update_count + excluded.confidence) / (tech_facts.update_count + 1),
			value = CAST(ROUND((tech_facts.confidence * tech_facts.update_count + excluded.confidence) / (tech_facts.update_count + 1) * 100) AS TEXT) || '%',
			update_count = tech_facts.update_count + 1,
			updated_at = excluded.updated_at
	`, f.ChatID, f.UserID, f.Category, f.Key, f.Value, f.Confidence, now, now)
	if err != nil {
		return fmt.Errorf("upserting tech fact: %w", err)
	}
	return nil
}

func (s *Store) GetTechFacts(ctx context.Context, chatID string) ([]domain.TechFact, error) {
	var facts []domain.TechFact
	q := s.db.NewSelect().Model(&facts)
	if chatID != "" {
		q = q.Where("chat_id = ?", chatID)
	}
	err := q.Order("category ASC", "key ASC").Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting tech facts: %w", err)
	}
	return facts, nil
}

func (s *Store) GetTechFactsByCategory(ctx context.Context, chatID, category string) ([]domain.TechFact, error) {
	var facts []domain.TechFact
	err := s.db.NewSelect().
		Model(&facts).
		Where("chat_id = ?", chatID).
		Where("category = ?", category).
		Order("key ASC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting tech facts by category: %w", err)
	}
	return facts, nil
}

func (s *Store) CountTechFacts(ctx context.Context, chatID string) (int, error) {
	q := s.db.NewSelect().Model((*domain.TechFact)(nil))
	if chatID != "" {
		q = q.Where("chat_id = ?", chatID)
	}
	count, err := q.Count(ctx)
	if err != nil {
		return 0, fmt.Errorf("counting tech facts: %w", err)
	}
	return count, nil
}

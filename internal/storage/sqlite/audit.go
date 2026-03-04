package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
)

func (s *Store) SaveAuditEntry(ctx context.Context, e *domain.AuditEntry) error {
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}
	_, err := s.db.NewInsert().Model(e).Exec(ctx)
	if err != nil {
		return fmt.Errorf("inserting audit entry: %w", err)
	}
	return nil
}

func (s *Store) IncrementUsage(ctx context.Context, chatID string, inputTokens, outputTokens int64) error {
	hour := time.Now().Truncate(time.Hour)

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO usage_stats (chat_id, hour, input_tokens, output_tokens, requests)
		VALUES (?, ?, ?, ?, 1)
		ON CONFLICT (chat_id, hour) DO UPDATE SET
			input_tokens = usage_stats.input_tokens + excluded.input_tokens,
			output_tokens = usage_stats.output_tokens + excluded.output_tokens,
			requests = usage_stats.requests + 1
	`, chatID, hour, inputTokens, outputTokens)
	if err != nil {
		return fmt.Errorf("incrementing usage: %w", err)
	}
	return nil
}

func (s *Store) GetUsageStats(ctx context.Context, chatID string) ([]domain.UsageRecord, error) {
	var records []domain.UsageRecord
	err := s.db.NewSelect().
		Model(&records).
		Where("chat_id = ?", chatID).
		Order("hour DESC").
		Limit(168). // last 7 days of hourly data
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("querying usage stats: %w", err)
	}
	return records, nil
}

// IncrementUsageWithCost increments usage stats including cost tracking.
func (s *Store) IncrementUsageWithCost(ctx context.Context, chatID string, inputTokens, outputTokens int64, costUSD float64) error {
	hour := time.Now().Truncate(time.Hour)

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO usage_stats (chat_id, hour, input_tokens, output_tokens, requests, cost_usd)
		VALUES (?, ?, ?, ?, 1, ?)
		ON CONFLICT (chat_id, hour) DO UPDATE SET
			input_tokens = usage_stats.input_tokens + excluded.input_tokens,
			output_tokens = usage_stats.output_tokens + excluded.output_tokens,
			requests = usage_stats.requests + 1,
			cost_usd = usage_stats.cost_usd + excluded.cost_usd
	`, chatID, hour, inputTokens, outputTokens, costUSD)
	if err != nil {
		return fmt.Errorf("incrementing usage with cost: %w", err)
	}
	return nil
}

// GetDailyCost returns the total cost in USD for the current day across all chats.
func (s *Store) GetDailyCost(ctx context.Context) (float64, error) {
	today := time.Now().Truncate(24 * time.Hour)
	var cost float64
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(cost_usd), 0) FROM usage_stats WHERE hour >= ?`, today).Scan(&cost)
	if err != nil {
		return 0, fmt.Errorf("querying daily cost: %w", err)
	}
	return cost, nil
}

// GetDailyCostByChat returns the total cost in USD for the current day for a specific chat.
func (s *Store) GetDailyCostByChat(ctx context.Context, chatID string) (float64, error) {
	today := time.Now().Truncate(24 * time.Hour)
	var cost float64
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(cost_usd), 0) FROM usage_stats WHERE chat_id = ? AND hour >= ?`,
		chatID, today).Scan(&cost)
	if err != nil {
		return 0, fmt.Errorf("querying daily cost for chat: %w", err)
	}
	return cost, nil
}

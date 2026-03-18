package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage"
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
	return s.UpsertUsage(ctx, storage.UsageUpsert{
		ChatID:       chatID,
		Hour:         time.Now().Truncate(time.Hour),
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		Requests:     1,
	})
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
	return s.UpsertUsage(ctx, storage.UsageUpsert{
		ChatID:       chatID,
		Hour:         time.Now().Truncate(time.Hour),
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		Requests:     1,
		CostUSD:      costUSD,
	})
}

// UpsertUsage inserts or updates a usage stats row with all fields.
func (s *Store) UpsertUsage(ctx context.Context, rec storage.UsageUpsert) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO usage_stats (chat_id, user_id, model, provider, hour,
			input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
			requests, cost_usd)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (chat_id, model, hour) DO UPDATE SET
			user_id = excluded.user_id,
			provider = excluded.provider,
			input_tokens = usage_stats.input_tokens + excluded.input_tokens,
			output_tokens = usage_stats.output_tokens + excluded.output_tokens,
			cache_read_tokens = usage_stats.cache_read_tokens + excluded.cache_read_tokens,
			cache_creation_tokens = usage_stats.cache_creation_tokens + excluded.cache_creation_tokens,
			requests = usage_stats.requests + excluded.requests,
			cost_usd = usage_stats.cost_usd + excluded.cost_usd
	`, rec.ChatID, rec.UserID, rec.Model, rec.Provider, rec.Hour,
		rec.InputTokens, rec.OutputTokens, rec.CacheReadTokens, rec.CacheCreationTokens,
		rec.Requests, rec.CostUSD)
	if err != nil {
		return fmt.Errorf("upserting usage: %w", err)
	}
	return nil
}

// GetUsageSummary returns aggregated usage summary matching the filter.
func (s *Store) GetUsageSummary(ctx context.Context, filter storage.UsageFilter) (*storage.UsageSummary, error) {
	query := `SELECT
		COALESCE(SUM(input_tokens), 0),
		COALESCE(SUM(output_tokens), 0),
		COALESCE(SUM(cache_read_tokens), 0),
		COALESCE(SUM(cache_creation_tokens), 0),
		COALESCE(SUM(requests), 0),
		COALESCE(SUM(cost_usd), 0)
		FROM usage_stats WHERE 1=1`

	args := usageFilterArgs(&query, filter)

	var summary storage.UsageSummary
	err := s.db.QueryRowContext(ctx, query, args...).Scan(
		&summary.TotalInputTokens, &summary.TotalOutputTokens,
		&summary.TotalCacheReadTokens, &summary.TotalCacheCreationTokens,
		&summary.TotalRequests, &summary.TotalCostUSD,
	)
	if err != nil {
		return nil, fmt.Errorf("querying usage summary: %w", err)
	}
	return &summary, nil
}

// GetUsageByDay returns daily aggregated usage matching the filter.
func (s *Store) GetUsageByDay(ctx context.Context, filter storage.UsageFilter) ([]storage.DailyUsage, error) {
	query := `SELECT
		strftime('%Y-%m-%d', datetime(hour)) AS date,
		COALESCE(SUM(input_tokens), 0),
		COALESCE(SUM(output_tokens), 0),
		COALESCE(SUM(cache_read_tokens), 0),
		COALESCE(SUM(cache_creation_tokens), 0),
		COALESCE(SUM(requests), 0),
		COALESCE(SUM(cost_usd), 0)
		FROM usage_stats WHERE 1=1`

	args := usageFilterArgs(&query, filter)
	query += ` GROUP BY date ORDER BY date DESC`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying usage by day: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var result []storage.DailyUsage
	for rows.Next() {
		var d storage.DailyUsage
		if err := rows.Scan(&d.Date, &d.InputTokens, &d.OutputTokens,
			&d.CacheReadTokens, &d.CacheCreationTokens, &d.Requests, &d.CostUSD); err != nil {
			return nil, fmt.Errorf("scanning daily usage: %w", err)
		}
		result = append(result, d)
	}
	return result, rows.Err()
}

// GetUsageByModel returns per-model aggregated usage matching the filter.
func (s *Store) GetUsageByModel(ctx context.Context, filter storage.UsageFilter) ([]storage.ModelUsage, error) {
	query := `SELECT
		model, provider,
		COALESCE(SUM(input_tokens), 0),
		COALESCE(SUM(output_tokens), 0),
		COALESCE(SUM(cache_read_tokens), 0),
		COALESCE(SUM(cache_creation_tokens), 0),
		COALESCE(SUM(requests), 0),
		COALESCE(SUM(cost_usd), 0)
		FROM usage_stats WHERE 1=1`

	args := usageFilterArgs(&query, filter)
	query += ` GROUP BY model, provider ORDER BY SUM(input_tokens) + SUM(output_tokens) DESC`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying usage by model: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var result []storage.ModelUsage
	for rows.Next() {
		var m storage.ModelUsage
		if err := rows.Scan(&m.Model, &m.Provider, &m.InputTokens, &m.OutputTokens,
			&m.CacheReadTokens, &m.CacheCreationTokens, &m.Requests, &m.CostUSD); err != nil {
			return nil, fmt.Errorf("scanning model usage: %w", err)
		}
		result = append(result, m)
	}
	return result, rows.Err()
}

// usageFilterArgs builds WHERE clauses and args for usage queries.
func usageFilterArgs(query *string, filter storage.UsageFilter) []any {
	var args []any
	if filter.ChatID != "" {
		*query += ` AND chat_id = ?`
		args = append(args, filter.ChatID)
	}
	if filter.UserID != "" {
		*query += ` AND user_id = ?`
		args = append(args, filter.UserID)
	}
	if filter.Model != "" {
		*query += ` AND model = ?`
		args = append(args, filter.Model)
	}
	if filter.Provider != "" {
		*query += ` AND provider = ?`
		args = append(args, filter.Provider)
	}
	if !filter.From.IsZero() {
		*query += ` AND hour >= ?`
		args = append(args, filter.From)
	}
	if !filter.To.IsZero() {
		*query += ` AND hour < ?`
		args = append(args, filter.To)
	}
	return args
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

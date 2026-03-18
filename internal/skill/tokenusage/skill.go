package tokenusage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/storage"
)

// TokenStatsSkill reports LLM token usage and cost statistics.
type TokenStatsSkill struct {
	store storage.Repository
}

// New creates a new TokenStatsSkill.
func New(store storage.Repository) *TokenStatsSkill {
	return &TokenStatsSkill{store: store}
}

// Name implements skill.Skill.
func (s *TokenStatsSkill) Name() string { return "token_stats" }

// Description implements skill.Skill.
func (s *TokenStatsSkill) Description() string {
	return "Show LLM token usage and cost statistics by day and model. Admin only."
}

// InputSchema implements skill.Skill.
func (s *TokenStatsSkill) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"period": {
				"type": "string",
				"enum": ["today", "week", "month", "all"],
				"description": "Time period: today, last 7 days (week, default), last 30 days (month), or all time"
			},
			"model": {
				"type": "string",
				"description": "Filter by model name (optional)"
			}
		}
	}`)
}

type statsInput struct {
	Period string `json:"period"`
	Model  string `json:"model"`
}

// Execute implements skill.Skill.
func (s *TokenStatsSkill) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	if skill.UserRoleFrom(ctx) != "admin" {
		return "Token usage statistics are only available to administrators.", nil
	}

	var in statsInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	filter := storage.UsageFilter{
		Model: in.Model,
	}

	now := time.Now()
	switch in.Period {
	case "today":
		y, m, d := now.Date()
		filter.From = time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	case "month":
		filter.From = now.AddDate(0, -1, 0)
	case "all":
		// no time filter
	default: // "week" or empty
		filter.From = now.AddDate(0, 0, -7)
	}

	summary, err := s.store.GetUsageSummary(ctx, filter)
	if err != nil {
		return "", fmt.Errorf("querying usage summary: %w", err)
	}

	daily, err := s.store.GetUsageByDay(ctx, filter)
	if err != nil {
		return "", fmt.Errorf("querying daily usage: %w", err)
	}

	models, err := s.store.GetUsageByModel(ctx, filter)
	if err != nil {
		return "", fmt.Errorf("querying model usage: %w", err)
	}

	var b strings.Builder

	// Summary
	periodLabels := map[string]string{
		"today": "today",
		"week":  "last 7 days",
		"month": "last 30 days",
		"all":   "all time",
	}
	period := in.Period
	if period == "" {
		period = "week"
	}
	label := periodLabels[period]
	if label == "" {
		label = period
	}
	fmt.Fprintf(&b, "## Token Usage Summary (%s)\n\n", label)
	fmt.Fprintf(&b, "- **Input tokens**: %d\n", summary.TotalInputTokens)
	fmt.Fprintf(&b, "- **Output tokens**: %d\n", summary.TotalOutputTokens)
	fmt.Fprintf(&b, "- **Cache read tokens**: %d\n", summary.TotalCacheReadTokens)
	fmt.Fprintf(&b, "- **Total requests**: %d\n", summary.TotalRequests)
	fmt.Fprintf(&b, "- **Total cost**: $%.4f\n\n", summary.TotalCostUSD)

	// By model
	if len(models) > 0 {
		b.WriteString("## By Model\n\n")
		b.WriteString("| Model | Provider | Input | Output | Cache Read | Requests | Cost |\n")
		b.WriteString("|-------|----------|-------|--------|------------|----------|------|\n")
		for _, m := range models {
			model := m.Model
			if model == "" {
				model = "(unknown)"
			}
			provider := m.Provider
			if provider == "" {
				provider = "-"
			}
			fmt.Fprintf(&b, "| %s | %s | %d | %d | %d | %d | $%.4f |\n",
				model, provider, m.InputTokens, m.OutputTokens, m.CacheReadTokens, m.Requests, m.CostUSD)
		}
		b.WriteString("\n")
	}

	// Daily breakdown
	if len(daily) > 0 {
		b.WriteString("## Daily Breakdown\n\n")
		b.WriteString("| Date | Input | Output | Cache Read | Requests | Cost |\n")
		b.WriteString("|------|-------|--------|------------|----------|------|\n")
		for _, d := range daily {
			fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | $%.4f |\n",
				d.Date, d.InputTokens, d.OutputTokens, d.CacheReadTokens, d.Requests, d.CostUSD)
		}
	}

	if summary.TotalRequests == 0 {
		return "No usage data available for the selected period.", nil
	}

	return b.String(), nil
}

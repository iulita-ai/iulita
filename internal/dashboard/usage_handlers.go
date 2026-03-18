package dashboard

import (
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/iulita-ai/iulita/internal/storage"
)

func parseUsageFilter(c *fiber.Ctx) storage.UsageFilter {
	f := storage.UsageFilter{
		ChatID:   c.Query("chat_id"),
		UserID:   c.Query("user_id"),
		Model:    c.Query("model"),
		Provider: c.Query("provider"),
	}
	if from := c.Query("from"); from != "" {
		if t, err := time.Parse("2006-01-02", from); err == nil {
			f.From = t
		} else if t, err := time.Parse(time.RFC3339, from); err == nil {
			f.From = t
		}
	}
	if to := c.Query("to"); to != "" {
		if t, err := time.Parse("2006-01-02", to); err == nil {
			// End of day — set to start of next day.
			f.To = t.AddDate(0, 0, 1)
		} else if t, err := time.Parse(time.RFC3339, to); err == nil {
			f.To = t
		}
	}
	return f
}

func (s *Server) handleUsageSummaryV2(c *fiber.Ctx) error {
	filter := parseUsageFilter(c)
	summary, err := s.store.GetUsageSummary(c.Context(), filter)
	if err != nil {
		return s.errorResponse(c, err)
	}
	return c.JSON(fiber.Map{
		"total_input_tokens":          summary.TotalInputTokens,
		"total_output_tokens":         summary.TotalOutputTokens,
		"total_cache_read_tokens":     summary.TotalCacheReadTokens,
		"total_cache_creation_tokens": summary.TotalCacheCreationTokens,
		"total_requests":              summary.TotalRequests,
		"total_cost_usd":              summary.TotalCostUSD,
	})
}

func (s *Server) handleUsageByDay(c *fiber.Ctx) error {
	filter := parseUsageFilter(c)
	rows, err := s.store.GetUsageByDay(c.Context(), filter)
	if err != nil {
		return s.errorResponse(c, err)
	}
	summary, err := s.store.GetUsageSummary(c.Context(), filter)
	if err != nil {
		return s.errorResponse(c, err)
	}
	return c.JSON(fiber.Map{
		"rows": rows,
		"summary": fiber.Map{
			"total_input_tokens":          summary.TotalInputTokens,
			"total_output_tokens":         summary.TotalOutputTokens,
			"total_cache_read_tokens":     summary.TotalCacheReadTokens,
			"total_cache_creation_tokens": summary.TotalCacheCreationTokens,
			"total_requests":              summary.TotalRequests,
			"total_cost_usd":              summary.TotalCostUSD,
		},
	})
}

func (s *Server) handleUsageByModel(c *fiber.Ctx) error {
	filter := parseUsageFilter(c)
	rows, err := s.store.GetUsageByModel(c.Context(), filter)
	if err != nil {
		return s.errorResponse(c, err)
	}
	return c.JSON(fiber.Map{
		"rows": rows,
	})
}

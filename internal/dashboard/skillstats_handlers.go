package dashboard

import (
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/iulita-ai/iulita/internal/storage"
)

func parseSkillStatsFilter(c *fiber.Ctx) storage.SkillStatsFilter {
	f := storage.SkillStatsFilter{
		UserID: c.Query("user_id"),
		Origin: c.Query("origin"),
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
			f.To = t.AddDate(0, 0, 1) // end of day → start of next day
		} else if t, err := time.Parse(time.RFC3339, to); err == nil {
			f.To = t
		}
	}
	return f
}

// handleSkillStats returns per-skill execution telemetry, plus a rollup totals row.
func (s *Server) handleSkillStats(c *fiber.Ctx) error {
	filter := parseSkillStatsFilter(c)
	rows, err := s.store.GetSkillStats(c.Context(), filter)
	if err != nil {
		return s.errorResponse(c, err)
	}

	var totalCalls, successCalls, failureCalls int64
	for _, r := range rows {
		totalCalls += r.TotalCalls
		successCalls += r.SuccessCalls
		failureCalls += r.FailureCalls
	}

	return c.JSON(fiber.Map{
		"rows": rows,
		"summary": fiber.Map{
			"skill_count":   len(rows),
			"total_calls":   totalCalls,
			"success_calls": successCalls,
			"failure_calls": failureCalls,
		},
	})
}

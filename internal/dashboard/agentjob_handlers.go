package dashboard

import (
	"github.com/gofiber/fiber/v2"

	"github.com/iulita-ai/iulita/internal/domain"
)

// handleListAgentJobs returns all agent jobs.
func (s *Server) handleListAgentJobs(c *fiber.Ctx) error {
	jobs, err := s.store.ListAgentJobs(c.Context())
	if err != nil {
		return s.errorResponse(c, err)
	}
	return c.JSON(jobs)
}

// handleGetAgentJob returns a single agent job by ID.
func (s *Server) handleGetAgentJob(c *fiber.Ctx) error {
	id, err := c.ParamsInt("id")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}
	job, err := s.store.GetAgentJob(c.Context(), int64(id))
	if err != nil {
		return s.errorResponse(c, err)
	}
	return c.JSON(job)
}

// handleCreateAgentJob creates a new agent job.
func (s *Server) handleCreateAgentJob(c *fiber.Ctx) error {
	var body struct {
		Name           string `json:"name"`
		Prompt         string `json:"prompt"`
		Model          string `json:"model"`
		CronExpr       string `json:"cron_expr"`
		Interval       string `json:"interval"`
		DeliveryChatID string `json:"delivery_chat_id"`
		Enabled        *bool  `json:"enabled"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}

	if body.Name == "" || body.Prompt == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "name and prompt are required"})
	}

	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}

	if body.Interval == "" {
		body.Interval = "24h"
	}

	job := &domain.AgentJob{
		Name:           body.Name,
		Prompt:         body.Prompt,
		Model:          body.Model,
		CronExpr:       body.CronExpr,
		Interval:       body.Interval,
		DeliveryChatID: body.DeliveryChatID,
		Enabled:        enabled,
	}

	if err := s.store.CreateAgentJob(c.Context(), job); err != nil {
		return s.errorResponse(c, err)
	}

	return c.Status(fiber.StatusCreated).JSON(job)
}

// handleUpdateAgentJob updates an existing agent job.
func (s *Server) handleUpdateAgentJob(c *fiber.Ctx) error {
	id, err := c.ParamsInt("id")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}

	job, err := s.store.GetAgentJob(c.Context(), int64(id))
	if err != nil {
		return s.errorResponse(c, err)
	}

	var body struct {
		Name           *string `json:"name"`
		Prompt         *string `json:"prompt"`
		Model          *string `json:"model"`
		CronExpr       *string `json:"cron_expr"`
		Interval       *string `json:"interval"`
		DeliveryChatID *string `json:"delivery_chat_id"`
		Enabled        *bool   `json:"enabled"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}

	if body.Name != nil {
		job.Name = *body.Name
	}
	if body.Prompt != nil {
		job.Prompt = *body.Prompt
	}
	if body.Model != nil {
		job.Model = *body.Model
	}
	if body.CronExpr != nil {
		job.CronExpr = *body.CronExpr
	}
	if body.Interval != nil {
		job.Interval = *body.Interval
	}
	if body.DeliveryChatID != nil {
		job.DeliveryChatID = *body.DeliveryChatID
	}
	if body.Enabled != nil {
		job.Enabled = *body.Enabled
	}

	if err := s.store.UpdateAgentJob(c.Context(), job); err != nil {
		return s.errorResponse(c, err)
	}

	return c.JSON(job)
}

// handleDeleteAgentJob deletes an agent job.
func (s *Server) handleDeleteAgentJob(c *fiber.Ctx) error {
	id, err := c.ParamsInt("id")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}

	if err := s.store.DeleteAgentJob(c.Context(), int64(id)); err != nil {
		return s.errorResponse(c, err)
	}

	return c.JSON(fiber.Map{"status": "deleted"})
}

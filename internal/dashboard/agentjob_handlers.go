package dashboard

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/gofiber/fiber/v2"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/modelruntime"
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
		ProfileID      string `json:"profile_id"`
		CronExpr       string `json:"cron_expr"`
		Interval       string `json:"interval"`
		DeliveryChatID string `json:"delivery_chat_id"`
		WakeGatePrompt string `json:"wake_gate_prompt"`
		Enabled        *bool  `json:"enabled"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}

	if body.Name == "" || body.Prompt == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "name and prompt are required"})
	}
	if err := s.validateAgentJobProfile(body.ProfileID); err != nil {
		return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{"error": err.Error()})
	}
	if body.ProfileID != "" {
		body.Model = ""
	} else if err := s.validateAgentJobLegacyHint(body.Model); err != nil {
		return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{"error": err.Error()})
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
		ProfileID:      body.ProfileID,
		CronExpr:       body.CronExpr,
		Interval:       body.Interval,
		DeliveryChatID: body.DeliveryChatID,
		WakeGatePrompt: body.WakeGatePrompt,
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
		Name           *string         `json:"name"`
		Prompt         *string         `json:"prompt"`
		Model          *string         `json:"model"`
		ProfileID      json.RawMessage `json:"profile_id"`
		CronExpr       *string         `json:"cron_expr"`
		Interval       *string         `json:"interval"`
		DeliveryChatID *string         `json:"delivery_chat_id"`
		WakeGatePrompt *string         `json:"wake_gate_prompt"`
		Enabled        *bool           `json:"enabled"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if len(body.ProfileID) != 0 {
		id := ""
		if !bytes.Equal(bytes.TrimSpace(body.ProfileID), []byte("null")) {
			if err := json.Unmarshal(body.ProfileID, &id); err != nil {
				return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "profile_id must be a string or null"})
			}
		}
		if err := s.validateAgentJobProfile(id); err != nil {
			return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{"error": err.Error()})
		}
		job.ProfileID = id
		// Explicit clearing means task default, not an old scalar vendor hint.
		job.Model = ""
		body.Model = nil
	}

	if body.Name != nil {
		job.Name = *body.Name
	}
	if body.WakeGatePrompt != nil {
		job.WakeGatePrompt = *body.WakeGatePrompt
	}
	if body.Prompt != nil {
		job.Prompt = *body.Prompt
	}
	if body.Model != nil {
		if err := s.validateAgentJobLegacyHint(*body.Model); err != nil {
			return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{"error": err.Error()})
		}
		job.Model = *body.Model
		job.ProfileID = ""
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

func (s *Server) validateAgentJobProfile(id string) error {
	if id == "" {
		return nil
	}
	if s.modelManager == nil {
		return errors.New("model profiles are unavailable")
	}
	if !agentJobProfileEligible(s.modelManager.Snapshot(), id) {
		return errors.New("select an active verified model profile")
	}
	return nil
}

func (s *Server) validateAgentJobLegacyHint(hint string) error {
	if hint == "" {
		return nil
	}
	if s.modelManager != nil {
		view := s.modelManager.Snapshot()
		if view.Settings.Policy.Everyday != "" {
			id := view.Settings.Policy.LegacyProfileHints[hint]
			role, mapped := view.Settings.Policy.LegacyHints[hint]
			if !mapped {
				switch hint {
				case llm.RouteHintCheap:
					role, mapped = "background", true
				case llm.RouteHintVision:
					role, mapped = "vision", true
				default:
					_, mapped = view.Settings.Policy.Roles()[hint]
					role = hint
				}
			}
			if id == "" && mapped {
				id = view.Settings.Policy.Roles()[role]
				if id == "" && role != "vision" {
					id = view.Settings.Policy.Everyday
				}
			}
			if id == "" || !agentJobProfileEligible(view, id) {
				return errors.New("legacy model hint is unavailable; select a verified profile")
			}
			return nil
		}
	}
	// Preserve documented legacy vendor hints and configured custom routes.
	for _, known := range []string{"claude", "claude-haiku", "openai", "deepseek", "ollama", "zai", "light"} {
		if hint == known {
			return nil
		}
	}
	if s.configStore != nil && s.configStore.Base() != nil {
		for _, route := range s.configStore.Base().Routing.Routes {
			if hint == route.Hint {
				return nil
			}
		}
	}
	return errors.New("unknown legacy model hint; use a configured route or verified profile")
}

func agentJobProfileEligible(view modelruntime.View, id string) bool {
	if view.Settings.Policy.Everyday == "" || view.ActiveRevision != view.Revision || view.ActivationStatus == "failed" {
		return false
	}
	provider := ""
	for _, profile := range view.Settings.Profiles {
		if profile.ID == id {
			provider = profile.Connection
			break
		}
	}
	if provider == "" {
		return false
	}
	for _, forbidden := range view.Settings.Policy.ForbiddenProviders {
		if forbidden == provider {
			return false
		}
	}
	available := false
	for _, connection := range view.Connections {
		if connection.Provider == provider && connection.Availability != "suspended" && connection.CredentialSet {
			available = true
			break
		}
	}
	if !available {
		return false
	}
	for _, profile := range view.Profiles {
		if profile.ID == id {
			return profile.Eligibility == "production_eligible" || profile.Eligibility == "legacy_preserved"
		}
	}
	return false
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

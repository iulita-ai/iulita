package dashboard

import (
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage"
)

// handleListSkillProposals returns self-authored skill proposals, optionally
// filtered by ?status=pending|rejected|discarded|installed.
func (s *Server) handleListSkillProposals(c *fiber.Ctx) error {
	rows, err := s.store.ListSkillProposals(c.Context(), storage.SkillProposalFilter{
		Status: c.Query("status"),
	})
	if err != nil {
		return s.errorResponse(c, err)
	}
	if rows == nil {
		rows = []domain.SkillProposal{}
	}
	return c.JSON(fiber.Map{"rows": rows})
}

// handleDiscardSkillProposal marks a proposal as discarded. Proposals are never
// auto-installed; this is a human dismissing a draft from the review queue.
func (s *Server) handleDiscardSkillProposal(c *fiber.Ctx) error {
	id, err := c.ParamsInt("id")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}
	if err := s.store.UpdateSkillProposalStatus(c.Context(), int64(id), domain.SkillProposalDiscarded); err != nil {
		return s.errorResponse(c, err)
	}
	return c.JSON(fiber.Map{"status": domain.SkillProposalDiscarded})
}

// handleInstallSkillProposal installs a pending proposal as a live text-only
// skill. This is the ONLY path that turns a proposal into an active skill, and
// it requires an explicit admin action — proposals are never auto-installed.
func (s *Server) handleInstallSkillProposal(c *fiber.Ctx) error {
	if s.skillManager == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "skill manager unavailable"})
	}
	id, err := c.ParamsInt("id")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}

	p, err := s.store.GetSkillProposal(c.Context(), int64(id))
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": err.Error()})
	}
	if p.Status != domain.SkillProposalPending {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "only pending proposals can be installed"})
	}

	var triggers []string
	if p.Triggers != "" {
		triggers = strings.Split(p.Triggers, ",")
	}

	installed, warnings, err := s.skillManager.InstallAuthored(c.Context(), p.Slug, p.Name, p.Description, p.Body, triggers)
	if err != nil {
		return s.errorResponse(c, err)
	}
	if err := s.store.UpdateSkillProposalStatus(c.Context(), int64(id), domain.SkillProposalInstalled); err != nil {
		return s.errorResponse(c, err)
	}

	return c.JSON(fiber.Map{
		"status":    domain.SkillProposalInstalled,
		"installed": installed,
		"warnings":  warnings,
	})
}

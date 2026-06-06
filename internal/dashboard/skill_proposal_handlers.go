package dashboard

import (
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

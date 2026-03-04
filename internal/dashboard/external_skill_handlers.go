package dashboard

import (
	"strings"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/domain"
)

// handleListExternalSkills returns all installed external skills.
func (s *Server) handleListExternalSkills(c *fiber.Ctx) error {
	skills, err := s.skillManager.ListInstalled(c.Context())
	if err != nil {
		return s.errorResponse(c, err)
	}
	if skills == nil {
		skills = []domain.InstalledSkill{}
	}
	return c.JSON(skills)
}

// handleGetMarketplaceSkillDetail resolves a marketplace skill's full metadata without installing.
func (s *Server) handleGetMarketplaceSkillDetail(c *fiber.Ctx) error {
	slug := c.Params("slug")
	if slug == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "slug is required"})
	}

	detail, err := s.skillManager.ResolveMarketplace(c.Context(), "clawhub", slug)
	if err != nil {
		if isNotFoundError(err) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": err.Error()})
		}
		return s.errorResponse(c, err)
	}

	return c.JSON(detail)
}

// handleGetExternalSkill returns a single installed skill by slug.
func (s *Server) handleGetExternalSkill(c *fiber.Ctx) error {
	slug := c.Params("slug")
	if slug == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "slug is required"})
	}

	sk, err := s.skillManager.GetInstalled(c.Context(), slug)
	if err != nil {
		if isNotFoundError(err) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": err.Error()})
		}
		return s.errorResponse(c, err)
	}

	return c.JSON(sk)
}

// handleInstallExternalSkill installs an external skill from a source.
func (s *Server) handleInstallExternalSkill(c *fiber.Ctx) error {
	var body struct {
		Source string `json:"source"`
		Ref    string `json:"ref"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if body.Ref == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "ref is required"})
	}
	if body.Source == "" {
		body.Source = "clawhub"
	}

	installed, warnings, err := s.skillManager.Install(c.Context(), body.Source, body.Ref)
	if err != nil {
		if isClientError(err) {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		return s.errorResponse(c, err)
	}

	if warnings == nil {
		warnings = []string{}
	}

	username := actorUsername(c)
	s.logger.Info("external skill installed",
		zap.String("slug", installed.Slug),
		zap.String("source", body.Source),
		zap.String("by", username),
	)

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"skill":    installed,
		"warnings": warnings,
	})
}

// handleUninstallExternalSkill removes an installed external skill.
func (s *Server) handleUninstallExternalSkill(c *fiber.Ctx) error {
	slug := c.Params("slug")
	if slug == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "slug is required"})
	}

	if err := s.skillManager.Uninstall(c.Context(), slug); err != nil {
		if isNotFoundError(err) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": err.Error()})
		}
		return s.errorResponse(c, err)
	}

	username := actorUsername(c)
	s.logger.Info("external skill uninstalled",
		zap.String("slug", slug),
		zap.String("by", username),
	)

	return c.JSON(fiber.Map{"status": "deleted", "slug": slug})
}

// handleSearchExternalSkills searches a marketplace for skills.
func (s *Server) handleSearchExternalSkills(c *fiber.Ctx) error {
	var body struct {
		Source string `json:"source"`
		Query  string `json:"query"`
		Limit  int    `json:"limit"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if body.Query == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "query is required"})
	}
	if body.Source == "" {
		body.Source = "clawhub"
	}
	if body.Limit <= 0 {
		body.Limit = 20
	}
	if body.Limit > 50 {
		body.Limit = 50
	}

	results, err := s.skillManager.Search(c.Context(), body.Source, body.Query, body.Limit)
	if err != nil {
		if isClientError(err) {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		return s.errorResponse(c, err)
	}

	if results == nil {
		results = []ExternalSkillResult{}
	}

	return c.JSON(fiber.Map{
		"results": results,
		"count":   len(results),
	})
}

// handleEnableExternalSkill enables an installed external skill.
func (s *Server) handleEnableExternalSkill(c *fiber.Ctx) error {
	slug := c.Params("slug")
	if slug == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "slug is required"})
	}

	if err := s.skillManager.Enable(c.Context(), slug); err != nil {
		if isNotFoundError(err) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": err.Error()})
		}
		return s.errorResponse(c, err)
	}

	username := actorUsername(c)
	s.logger.Info("external skill enabled",
		zap.String("slug", slug),
		zap.String("by", username),
	)

	return c.JSON(fiber.Map{"status": "ok", "slug": slug, "enabled": true})
}

// handleDisableExternalSkill disables an installed external skill.
func (s *Server) handleDisableExternalSkill(c *fiber.Ctx) error {
	slug := c.Params("slug")
	if slug == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "slug is required"})
	}

	if err := s.skillManager.Disable(c.Context(), slug); err != nil {
		if isNotFoundError(err) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": err.Error()})
		}
		return s.errorResponse(c, err)
	}

	username := actorUsername(c)
	s.logger.Info("external skill disabled",
		zap.String("slug", slug),
		zap.String("by", username),
	)

	return c.JSON(fiber.Map{"status": "ok", "slug": slug, "enabled": false})
}

// handleUpdateExternalSkill re-installs an external skill to update it.
func (s *Server) handleUpdateExternalSkill(c *fiber.Ctx) error {
	slug := c.Params("slug")
	if slug == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "slug is required"})
	}

	// Fetch current skill metadata.
	existing, err := s.skillManager.GetInstalled(c.Context(), slug)
	if err != nil {
		if isNotFoundError(err) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": err.Error()})
		}
		return s.errorResponse(c, err)
	}

	source := existing.Source
	sourceRef := existing.SourceRef

	// Uninstall old version.
	if err := s.skillManager.Uninstall(c.Context(), slug); err != nil {
		return s.errorResponse(c, err)
	}

	// Re-install from original source.
	installed, warnings, err := s.skillManager.Install(c.Context(), source, sourceRef)
	if err != nil {
		s.logger.Error("reinstall failed after uninstall",
			zap.String("slug", slug),
			zap.String("source", source),
			zap.Error(err),
		)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "reinstall failed after removing old version — manual reinstall required",
			"phase": "reinstall",
		})
	}

	if warnings == nil {
		warnings = []string{}
	}

	username := actorUsername(c)
	s.logger.Info("external skill updated",
		zap.String("slug", slug),
		zap.String("source", source),
		zap.String("by", username),
	)

	return c.JSON(fiber.Map{
		"skill":    installed,
		"warnings": warnings,
	})
}

// actorUsername extracts the username from JWT claims.
func actorUsername(c *fiber.Ctx) string {
	claims := auth.GetClaims(c)
	if claims != nil {
		return claims.Username
	}
	return "unknown"
}

// isNotFoundError checks if an error indicates a not-found condition.
func isNotFoundError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "not found") || strings.Contains(msg, "no rows")
}

// isClientError checks if an error is a client-side error (bad input).
func isClientError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "unknown source") ||
		strings.Contains(msg, "disabled") ||
		strings.Contains(msg, "max installed") ||
		strings.Contains(msg, "invalid skill slug")
}

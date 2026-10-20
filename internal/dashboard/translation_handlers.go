package dashboard

import (
	"github.com/gofiber/fiber/v2"
	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/skill"
)

func translationAdmin(c *fiber.Ctx) bool {
	claims := auth.GetClaims(c)
	return claims != nil && claims.Role == domain.RoleAdmin
}

func (s *Server) skillServiceStatus(name string) *skill.ServiceStatus {
	if s.registry == nil {
		return nil
	}
	native, ok := s.registry.Registered(name)
	if !ok {
		return nil
	}
	describer, ok := native.(skill.ServiceStatusDeclarer)
	if !ok {
		return nil
	}
	status := describer.ServiceStatus()
	for _, entry := range s.registry.AllSkills() {
		if entry.Skill.Name() == name && !entry.Enabled {
			status.State = "disabled"
			break
		}
	}
	return &status
}

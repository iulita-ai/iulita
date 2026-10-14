package dashboard

import (
	"context"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/domain"
)

// HTTPStandingGrantStore is the admin view of standing approvals.
type HTTPStandingGrantStore interface {
	ListHTTPStandingGrants(ctx context.Context, owner string) ([]domain.HTTPStandingGrant, error)
	RevokeHTTPStandingGrant(ctx context.Context, id int64) error
}

// handleListStandingGrants returns one owner's standing approvals (admin or
// the owner themself — a user must be able to see what they granted).
func (s *Server) handleListStandingGrants(c *fiber.Ctx) error {
	claims := auth.GetClaims(c)
	if claims == nil || claims.UserID == "" {
		return fiber.ErrUnauthorized
	}
	owner := claims.UserID
	// Admins may inspect any owner's grants via ?owner=.
	if claims.Role == domain.RoleAdmin {
		if q := c.Query("owner"); q != "" {
			owner = q
		}
	}
	grants, err := s.standingGrants.ListHTTPStandingGrants(c.Context(), owner)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "listing standing grants failed"})
	}
	type view struct {
		ID        int64     `json:"id"`
		Scope     string    `json:"scope"`
		Origin    string    `json:"origin,omitempty"`
		ExpiresAt time.Time `json:"expires_at"`
		Revoked   bool      `json:"revoked"`
	}
	out := make([]view, 0, len(grants))
	for i := range grants {
		g := &grants[i]
		v := view{ID: g.ID, Scope: g.Scope, ExpiresAt: g.ExpiresAt, Revoked: g.RevokedAt != nil}
		if g.Scope == domain.HTTPStandingScopeOrigin {
			v.Origin = g.Scheme + "://" + g.Host + ":" + g.Port
		}
		out = append(out, v)
	}
	return c.JSON(out)
}

// handleRevokeStandingGrant revokes a standing approval. Admins revoke any;
// regular users may revoke their own (the grant table is owner-scoped).
func (s *Server) handleRevokeStandingGrant(c *fiber.Ctx) error {
	claims := auth.GetClaims(c)
	if claims == nil || claims.UserID == "" {
		return fiber.ErrUnauthorized
	}
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		return fiber.ErrBadRequest
	}
	grants, err := s.standingGrants.ListHTTPStandingGrants(c.Context(), claims.UserID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "lookup failed"})
	}
	owns := false
	for i := range grants {
		if grants[i].ID == id {
			owns = true
			break
		}
	}
	if !owns && claims.Role != domain.RoleAdmin {
		return fiber.ErrNotFound // foreign grants are indistinguishable
	}
	if err := s.standingGrants.RevokeHTTPStandingGrant(c.Context(), id); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "revoke failed"})
	}
	return c.SendStatus(fiber.StatusNoContent)
}

package auth

import (
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/iulita-ai/iulita/internal/domain"
)

const (
	// ContextKeyUser is the fiber locals key for the authenticated user claims.
	ContextKeyUser = "auth_user"
)

// FiberMiddleware returns a GoFiber middleware that validates JWT tokens.
func FiberMiddleware(svc *Service) fiber.Handler {
	return func(c *fiber.Ctx) error {
		auth := c.Get("Authorization")
		if auth == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "authorization required"})
		}

		token := strings.TrimPrefix(auth, "Bearer ")
		if token == auth {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "bearer token required"})
		}

		claims, err := svc.ValidateToken(token)
		if err != nil {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "invalid or expired token"})
		}

		c.Locals(ContextKeyUser, claims)
		return c.Next()
	}
}

// AdminOnly returns a middleware that restricts access to admin users.
func AdminOnly() fiber.Handler {
	return func(c *fiber.Ctx) error {
		claims, ok := c.Locals(ContextKeyUser).(*Claims)
		if !ok || claims.Role != domain.RoleAdmin {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "admin access required"})
		}
		return c.Next()
	}
}

// GetClaims extracts auth claims from a fiber context.
func GetClaims(c *fiber.Ctx) *Claims {
	claims, _ := c.Locals(ContextKeyUser).(*Claims)
	return claims
}

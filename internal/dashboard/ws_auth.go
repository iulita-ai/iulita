package dashboard

import (
	"net/url"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/domain"
)

// Browser WebSockets cannot set Authorization headers. Accept their access
// token at upgrade, then derive identity from the current database user.
func (s *Server) authenticateWebSocket(c *fiber.Ctx) error {
	if s.authService == nil || s.store == nil {
		return fiber.ErrUnauthorized
	}
	if origin := c.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		// Traefik forwards WebSocket transport schemes, while browsers send
		// the HTTP(S) page origin. Preserve the secure/insecure distinction.
		scheme := c.Protocol()
		switch scheme {
		case "ws":
			scheme = "http"
		case "wss":
			scheme = "https"
		}
		if err != nil || u.Scheme != scheme || !strings.EqualFold(u.Host, c.Hostname()) {
			return fiber.ErrForbidden
		}
	}
	token := c.Query("token")
	if header := c.Get("Authorization"); header != "" {
		if !strings.HasPrefix(header, "Bearer ") {
			return fiber.ErrUnauthorized
		}
		token = strings.TrimPrefix(header, "Bearer ")
	}
	claims, err := s.authService.ValidateToken(token)
	if err != nil || claims.ExpiresAt == nil || claims.UserID == "" {
		return fiber.ErrUnauthorized
	}
	user, err := s.store.GetUser(c.UserContext(), claims.UserID)
	if err != nil || user == nil || user.ID != claims.UserID {
		return fiber.ErrUnauthorized
	}
	if user.MustChangePass || c.Path() == "/ws" && user.Role != domain.RoleAdmin {
		return fiber.ErrForbidden
	}
	if id := c.Query("user_id"); id != "" && id != user.ID {
		return fiber.ErrForbidden
	}
	if id := c.Query("chat_id"); id != "" && id != "web:"+user.ID {
		return fiber.ErrForbidden
	}
	claims.Username, claims.Role = user.Username, user.Role
	c.Locals(auth.ContextKeyUser, claims)
	return c.Next()
}

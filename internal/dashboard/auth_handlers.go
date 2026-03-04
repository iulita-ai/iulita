package dashboard

import (
	"github.com/gofiber/fiber/v2"

	"github.com/iulita-ai/iulita/internal/auth"
)

func (s *Server) handleLogin(c *fiber.Ctx) error {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if body.Username == "" || body.Password == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "username and password required"})
	}

	accessToken, refreshToken, mustChange, err := s.authService.Login(c.Context(), body.Username, body.Password)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "invalid credentials"})
	}

	return c.JSON(fiber.Map{
		"access_token":         accessToken,
		"refresh_token":        refreshToken,
		"must_change_password": mustChange,
	})
}

func (s *Server) handleRefresh(c *fiber.Ctx) error {
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if body.RefreshToken == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "refresh_token required"})
	}

	newToken, err := s.authService.RefreshToken(c.Context(), body.RefreshToken)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "invalid refresh token"})
	}

	return c.JSON(fiber.Map{"access_token": newToken})
}

func (s *Server) handleChangePassword(c *fiber.Ctx) error {
	claims := auth.GetClaims(c)
	if claims == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "not authenticated"})
	}

	var body struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if body.OldPassword == "" || body.NewPassword == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "old_password and new_password required"})
	}
	if len(body.NewPassword) < 6 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "password must be at least 6 characters"})
	}

	if err := s.authService.ChangePassword(c.Context(), claims.UserID, body.OldPassword, body.NewPassword); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}

	return c.JSON(fiber.Map{"status": "password changed"})
}

func (s *Server) handleMe(c *fiber.Ctx) error {
	claims := auth.GetClaims(c)
	if claims == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "not authenticated"})
	}

	user, err := s.store.GetUser(c.Context(), claims.UserID)
	if err != nil || user == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "user not found"})
	}

	resp := fiber.Map{
		"id":               user.ID,
		"username":         user.Username,
		"role":             user.Role,
		"display_name":     user.DisplayName,
		"timezone":         user.Timezone,
		"must_change_pass": user.MustChangePass,
		"created_at":       user.CreatedAt,
		"needs_setup":      s.setupMode,
	}
	return c.JSON(resp)
}

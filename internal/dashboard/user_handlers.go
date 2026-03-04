package dashboard

import (
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/i18n"
)

func (s *Server) handleListUsers(c *fiber.Ctx) error {
	users, err := s.store.ListUsers(c.Context())
	if err != nil {
		return s.errorResponse(c, err)
	}
	return c.JSON(users)
}

func (s *Server) handleGetUser(c *fiber.Ctx) error {
	user, err := s.store.GetUser(c.Context(), c.Params("id"))
	if err != nil {
		return s.errorResponse(c, err)
	}
	if user == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "user not found"})
	}
	return c.JSON(user)
}

func (s *Server) handleCreateUser(c *fiber.Ctx) error {
	var body struct {
		Username    string          `json:"username"`
		Password    string          `json:"password"`
		Role        domain.UserRole `json:"role"`
		DisplayName string          `json:"display_name"`
		Timezone    string          `json:"timezone"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if body.Username == "" || body.Password == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "username and password required"})
	}
	if len(body.Password) < 6 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "password must be at least 6 characters"})
	}
	if body.Role == "" {
		body.Role = domain.RoleRegular
	}
	if body.Role != domain.RoleAdmin && body.Role != domain.RoleRegular {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "role must be 'admin' or 'user'"})
	}

	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		return s.errorResponse(c, err)
	}

	tz := body.Timezone
	if tz == "" {
		tz = "UTC"
	}

	user := &domain.User{
		ID:             uuid.Must(uuid.NewV7()).String(),
		Username:       body.Username,
		PasswordHash:   hash,
		Role:           body.Role,
		DisplayName:    body.DisplayName,
		Timezone:       tz,
		MustChangePass: true,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	if err := s.store.CreateUser(c.Context(), user); err != nil {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": err.Error()})
	}

	return c.Status(fiber.StatusCreated).JSON(user)
}

func (s *Server) handleUpdateUser(c *fiber.Ctx) error {
	user, err := s.store.GetUser(c.Context(), c.Params("id"))
	if err != nil {
		return s.errorResponse(c, err)
	}
	if user == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "user not found"})
	}

	var body struct {
		Username    *string          `json:"username"`
		Password    *string          `json:"password"`
		Role        *domain.UserRole `json:"role"`
		DisplayName *string          `json:"display_name"`
		Timezone    *string          `json:"timezone"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}

	if body.Username != nil {
		user.Username = *body.Username
	}
	if body.Password != nil {
		if len(*body.Password) < 6 {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "password must be at least 6 characters"})
		}
		hash, err := auth.HashPassword(*body.Password)
		if err != nil {
			return s.errorResponse(c, err)
		}
		user.PasswordHash = hash
		user.MustChangePass = false
	}
	if body.Role != nil {
		user.Role = *body.Role
	}
	if body.DisplayName != nil {
		user.DisplayName = *body.DisplayName
	}
	if body.Timezone != nil {
		user.Timezone = *body.Timezone
	}
	user.UpdatedAt = time.Now()

	if err := s.store.UpdateUser(c.Context(), user); err != nil {
		return s.errorResponse(c, err)
	}
	return c.JSON(user)
}

func (s *Server) handleSetLocale(c *fiber.Ctx) error {
	claims := auth.GetClaims(c)
	if claims == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	var body struct {
		Locale string `json:"locale"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if body.Locale == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "locale required"})
	}
	if !i18n.IsSupported(body.Locale) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "unsupported locale"})
	}

	// Update locale on all user's webchat channels.
	channels, err := s.store.ListUserChannels(c.Context(), claims.UserID)
	if err != nil {
		return s.errorResponse(c, err)
	}
	for _, ch := range channels {
		if ch.ChannelType == "webchat" {
			if err := s.store.UpdateChannelLocale(c.Context(), ch.ChannelID, body.Locale); err != nil {
				return s.errorResponse(c, err)
			}
		}
	}

	return c.JSON(fiber.Map{"locale": body.Locale})
}

func (s *Server) handleDeleteUser(c *fiber.Ctx) error {
	id := c.Params("id")

	// Prevent admin from deleting themselves.
	claims := auth.GetClaims(c)
	if claims != nil && claims.UserID == id {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "cannot delete yourself"})
	}

	if err := s.store.DeleteUser(c.Context(), id); err != nil {
		return s.errorResponse(c, err)
	}
	return c.JSON(fiber.Map{"status": "deleted"})
}

func (s *Server) handleListUserChannels(c *fiber.Ctx) error {
	channels, err := s.store.ListUserChannels(c.Context(), c.Params("id"))
	if err != nil {
		return s.errorResponse(c, err)
	}
	return c.JSON(channels)
}

func (s *Server) handleBindChannel(c *fiber.Ctx) error {
	userID := c.Params("id")

	var body struct {
		ChannelType     string `json:"channel_type"`
		ChannelID       string `json:"channel_id"`
		ChannelUserID   string `json:"channel_user_id"`
		ChannelUsername string `json:"channel_username"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if body.ChannelType == "" || body.ChannelUserID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "channel_type and channel_user_id required"})
	}

	ch := &domain.UserChannel{
		UserID:          userID,
		ChannelType:     body.ChannelType,
		ChannelID:       body.ChannelID,
		ChannelUserID:   body.ChannelUserID,
		ChannelUsername: body.ChannelUsername,
		Enabled:         true,
	}

	if err := s.store.BindChannel(c.Context(), ch); err != nil {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": err.Error()})
	}
	return c.Status(fiber.StatusCreated).JSON(ch)
}

func (s *Server) handleUnbindChannel(c *fiber.Ctx) error {
	channelID, err := strconv.ParseInt(c.Params("channel_id"), 10, 64)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid channel_id"})
	}

	if err := s.store.UnbindChannel(c.Context(), channelID); err != nil {
		return s.errorResponse(c, err)
	}
	return c.JSON(fiber.Map{"status": "unbound"})
}

package dashboard

import (
	"strings"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/domain"
)

// channelInstanceResponse is the API response for a channel instance.
// Config is masked for source=config channels.
type channelInstanceResponse struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Name      string `json:"name"`
	Config    string `json:"config"`
	Source    string `json:"source"`
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func instanceToResponse(ci *domain.ChannelInstance) channelInstanceResponse {
	cfg := ci.Config
	if ci.Source == domain.ChannelSourceConfig {
		cfg = "" // hide config for TOML-sourced channels
	}
	return channelInstanceResponse{
		ID:        ci.ID,
		Type:      ci.Type,
		Name:      ci.Name,
		Config:    cfg,
		Source:    ci.Source,
		Enabled:   ci.Enabled,
		CreatedAt: ci.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt: ci.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
}

// handleListChannelInstances returns all channel instances.
func (s *Server) handleListChannelInstances(c *fiber.Ctx) error {
	instances, err := s.store.ListChannelInstances(c.Context())
	if err != nil {
		return s.errorResponse(c, err)
	}

	result := make([]channelInstanceResponse, 0, len(instances))
	for i := range instances {
		result = append(result, instanceToResponse(&instances[i]))
	}
	return c.JSON(result)
}

// handleGetChannelInstance returns a single channel instance by ID.
func (s *Server) handleGetChannelInstance(c *fiber.Ctx) error {
	id := c.Params("id")
	ci, err := s.store.GetChannelInstance(c.Context(), id)
	if err != nil {
		return s.errorResponse(c, err)
	}
	if ci == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "channel instance not found"})
	}
	return c.JSON(instanceToResponse(ci))
}

// handleCreateChannelInstance creates a new dashboard-sourced channel instance.
func (s *Server) handleCreateChannelInstance(c *fiber.Ctx) error {
	var body struct {
		ID     string `json:"id"`
		Type   string `json:"type"`
		Name   string `json:"name"`
		Config string `json:"config"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}

	body.ID = strings.TrimSpace(body.ID)
	body.Type = strings.TrimSpace(body.Type)
	body.Name = strings.TrimSpace(body.Name)

	if body.ID == "" || body.Type == "" || body.Name == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "id, type, and name are required"})
	}

	// Check for duplicate ID.
	existing, err := s.store.GetChannelInstance(c.Context(), body.ID)
	if err != nil {
		return s.errorResponse(c, err)
	}
	if existing != nil {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "channel instance with this ID already exists"})
	}

	// Encrypt config if encryption is available.
	configValue := body.Config
	if configValue == "" {
		configValue = "{}"
	}
	if s.configStore != nil && s.configStore.EncryptionEnabled() {
		encrypted, err := s.configStore.Encrypt(configValue)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to encrypt config"})
		}
		configValue = encrypted
	}

	ci := &domain.ChannelInstance{
		ID:      body.ID,
		Type:    body.Type,
		Name:    body.Name,
		Config:  configValue,
		Source:  domain.ChannelSourceDashboard,
		Enabled: true,
	}

	if err := s.store.CreateChannelInstance(c.Context(), ci); err != nil {
		return s.errorResponse(c, err)
	}

	// Trigger runtime startup if manager is available.
	if s.channelManager != nil {
		if err := s.channelManager.AddInstance(c.Context(), *ci); err != nil {
			s.logger.Error("failed to start new channel instance at runtime",
				zap.String("id", ci.ID), zap.Error(err))
			// Don't return error — DB record was created; runtime start will retry on next boot.
		}
	}

	return c.Status(fiber.StatusCreated).JSON(instanceToResponse(ci))
}

// handleUpdateChannelInstance updates a channel instance.
// source=config: only enabled can be toggled.
// source=dashboard: all fields can be updated.
func (s *Server) handleUpdateChannelInstance(c *fiber.Ctx) error {
	id := c.Params("id")
	ci, err := s.store.GetChannelInstance(c.Context(), id)
	if err != nil {
		return s.errorResponse(c, err)
	}
	if ci == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "channel instance not found"})
	}

	var body struct {
		Name    *string `json:"name"`
		Config  *string `json:"config"`
		Enabled *bool   `json:"enabled"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}

	if ci.Source == domain.ChannelSourceConfig {
		// Config-sourced: only toggle enabled.
		if body.Enabled != nil {
			ci.Enabled = *body.Enabled
		}
	} else {
		// Dashboard-sourced: update all fields.
		if body.Name != nil {
			ci.Name = *body.Name
		}
		if body.Enabled != nil {
			ci.Enabled = *body.Enabled
		}
		if body.Config != nil {
			configValue := *body.Config
			if s.configStore != nil && s.configStore.EncryptionEnabled() {
				encrypted, err := s.configStore.Encrypt(configValue)
				if err != nil {
					return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to encrypt config"})
				}
				configValue = encrypted
			}
			ci.Config = configValue
		}
	}

	if err := s.store.UpdateChannelInstance(c.Context(), ci); err != nil {
		return s.errorResponse(c, err)
	}

	// Trigger runtime update if manager is available.
	if s.channelManager != nil {
		if err := s.channelManager.UpdateInstance(c.Context(), *ci); err != nil {
			s.logger.Error("failed to update channel instance at runtime",
				zap.String("id", ci.ID), zap.Error(err))
		}
	}

	return c.JSON(instanceToResponse(ci))
}

// handleDeleteChannelInstance deletes a dashboard-sourced channel instance.
func (s *Server) handleDeleteChannelInstance(c *fiber.Ctx) error {
	id := c.Params("id")
	ci, err := s.store.GetChannelInstance(c.Context(), id)
	if err != nil {
		return s.errorResponse(c, err)
	}
	if ci == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "channel instance not found"})
	}

	if ci.Source == domain.ChannelSourceConfig {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "cannot delete config-sourced channel instance"})
	}

	// Stop the channel instance at runtime if manager is available.
	if s.channelManager != nil {
		s.channelManager.StopInstance(id)
	}

	if err := s.store.DeleteChannelInstance(c.Context(), id); err != nil {
		return s.errorResponse(c, err)
	}

	return c.JSON(fiber.Map{"status": "deleted"})
}

// handleListChannelBindings returns user bindings for a channel instance.
func (s *Server) handleListChannelBindings(c *fiber.Ctx) error {
	instanceID := c.Params("id")

	// Verify instance exists.
	ci, err := s.store.GetChannelInstance(c.Context(), instanceID)
	if err != nil {
		return s.errorResponse(c, err)
	}
	if ci == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "channel instance not found"})
	}

	// List all channel bindings and filter by channel type matching instance type.
	allBindings, err := s.store.ListAllChannels(c.Context())
	if err != nil {
		return s.errorResponse(c, err)
	}

	// Build user lookup.
	users, err := s.store.ListUsers(c.Context())
	if err != nil {
		return s.errorResponse(c, err)
	}
	userMap := make(map[string][2]string, len(users))
	for _, u := range users {
		userMap[u.ID] = [2]string{u.Username, u.DisplayName}
	}

	type bindingWithUser struct {
		ID               int64  `json:"id"`
		UserID           string `json:"user_id"`
		ChannelType      string `json:"channel_type"`
		ChannelID        string `json:"channel_id"`
		ChannelUserID    string `json:"channel_user_id"`
		ChannelUsername  string `json:"channel_username"`
		Enabled          bool   `json:"enabled"`
		CreatedAt        string `json:"created_at"`
		OwnerUsername    string `json:"owner_username"`
		OwnerDisplayName string `json:"owner_display_name"`
	}

	result := make([]bindingWithUser, 0)
	for _, b := range allBindings {
		if b.ChannelType != ci.Type {
			continue
		}
		owner := userMap[b.UserID]
		result = append(result, bindingWithUser{
			ID:               b.ID,
			UserID:           b.UserID,
			ChannelType:      b.ChannelType,
			ChannelID:        b.ChannelID,
			ChannelUserID:    b.ChannelUserID,
			ChannelUsername:  b.ChannelUsername,
			Enabled:          b.Enabled,
			CreatedAt:        b.CreatedAt.Format("2006-01-02T15:04:05Z"),
			OwnerUsername:    owner[0],
			OwnerDisplayName: owner[1],
		})
	}

	return c.JSON(result)
}

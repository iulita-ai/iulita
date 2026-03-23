package dashboard

import (
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/iulita-ai/iulita/internal/credential"
	"github.com/iulita-ai/iulita/internal/domain"
)

func (s *Server) handleListCredentials(c *fiber.Ctx) error {
	filter := credential.CredentialFilter{
		Scope:   domain.CredentialScope(c.Query("scope")),
		Type:    domain.CredentialType(c.Query("type")),
		OwnerID: c.Query("owner_id"),
	}
	views, err := s.credentialManager.ListFromDB(c.Context(), filter)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}
	if views == nil {
		views = []credential.CredentialView{}
	}
	return c.JSON(views)
}

func (s *Server) handleCreateCredential(c *fiber.Ctx) error {
	var req struct {
		Name        string                 `json:"name"`
		Type        domain.CredentialType  `json:"type"`
		Scope       domain.CredentialScope `json:"scope"`
		OwnerID     string                 `json:"owner_id"`
		Value       string                 `json:"value"`
		Description string                 `json:"description"`
		Tags        []string               `json:"tags"`
		ExpiresAt   *string                `json:"expires_at"`
	}
	if err := c.BodyParser(&req); err != nil {
		return fiber.ErrBadRequest
	}
	if req.Name == "" || req.Value == "" {
		return fiber.NewError(fiber.StatusBadRequest, "name and value are required")
	}
	var expiresAt *time.Time
	if req.ExpiresAt != nil && *req.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, *req.ExpiresAt)
		if err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "invalid expires_at format (use RFC3339)")
		}
		expiresAt = &t
	}
	actor := actorFromCtx(c)
	cred, err := s.credentialManager.Set(c.Context(), credential.SetRequest{
		Name:        req.Name,
		Type:        req.Type,
		Scope:       req.Scope,
		OwnerID:     req.OwnerID,
		Value:       req.Value,
		Description: req.Description,
		Tags:        req.Tags,
		UpdatedBy:   actor,
		ExpiresAt:   expiresAt,
	})
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}
	return c.Status(fiber.StatusCreated).JSON(credential.ToView(cred))
}

func (s *Server) handleGetCredential(c *fiber.Ctx) error {
	id, err := parseCredID(c)
	if err != nil {
		return err
	}
	cred, err := s.credentialManager.GetByID(c.Context(), id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return fiber.NewError(fiber.StatusNotFound, err.Error())
		}
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}
	bindings, _ := s.credentialManager.ListBindings(c.Context(), id) //nolint:errcheck // best-effort enrichment
	if bindings == nil {
		bindings = []domain.CredentialBinding{}
	}
	return c.JSON(credential.CredentialDetailView{
		CredentialView: credential.ToView(cred),
		Bindings:       bindings,
	})
}

func (s *Server) handleUpdateCredential(c *fiber.Ctx) error {
	id, err := parseCredID(c)
	if err != nil {
		return err
	}
	var req struct {
		Value       string   `json:"value"`
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
	}
	if parseErr := c.BodyParser(&req); parseErr != nil {
		return fiber.ErrBadRequest
	}
	existing, err := s.credentialManager.GetByID(c.Context(), id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return fiber.NewError(fiber.StatusNotFound, err.Error())
		}
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}
	if req.Value == "" || req.Value == "***" {
		return fiber.NewError(fiber.StatusBadRequest, "value is required (use POST /:id/rotate to change value only)")
	}
	actor := actorFromCtx(c)
	_, err = s.credentialManager.Set(c.Context(), credential.SetRequest{
		Name:        existing.Name,
		Type:        existing.Type,
		Scope:       existing.Scope,
		OwnerID:     existing.OwnerID,
		Value:       req.Value,
		Description: req.Description,
		Tags:        req.Tags,
		UpdatedBy:   actor,
	})
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func (s *Server) handleDeleteCredential(c *fiber.Ctx) error {
	id, err := parseCredID(c)
	if err != nil {
		return err
	}
	if err := s.credentialManager.Delete(c.Context(), id, actorFromCtx(c)); err != nil {
		if strings.Contains(err.Error(), "not found") {
			return fiber.NewError(fiber.StatusNotFound, err.Error())
		}
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func (s *Server) handleRotateCredential(c *fiber.Ctx) error {
	id, err := parseCredID(c)
	if err != nil {
		return err
	}
	var req struct {
		NewValue string `json:"new_value"`
	}
	if err := c.BodyParser(&req); err != nil {
		return fiber.ErrBadRequest
	}
	if req.NewValue == "" || req.NewValue == "***" {
		return fiber.NewError(fiber.StatusBadRequest, "new_value is required")
	}
	if err := s.credentialManager.Rotate(c.Context(), credential.RotateRequest{
		ID:        id,
		NewValue:  req.NewValue,
		UpdatedBy: actorFromCtx(c),
	}); err != nil {
		if strings.Contains(err.Error(), "not found") {
			return fiber.NewError(fiber.StatusNotFound, err.Error())
		}
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func (s *Server) handleListCredentialAudit(c *fiber.Ctx) error {
	id, err := parseCredID(c)
	if err != nil {
		return err
	}
	limit := 50
	if v := c.QueryInt("limit", 50); v > 0 {
		limit = v
	}
	entries, err := s.credentialManager.ListCredentialAudit(c.Context(), id, limit)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}
	if entries == nil {
		entries = []domain.CredentialAudit{}
	}
	return c.JSON(entries)
}

func (s *Server) handleListCredentialBindings(c *fiber.Ctx) error {
	id, err := parseCredID(c)
	if err != nil {
		return err
	}
	bindings, err := s.credentialManager.ListBindings(c.Context(), id)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}
	if bindings == nil {
		bindings = []domain.CredentialBinding{}
	}
	return c.JSON(bindings)
}

func (s *Server) handleBindCredential(c *fiber.Ctx) error {
	id, err := parseCredID(c)
	if err != nil {
		return err
	}
	var req struct {
		ConsumerType string `json:"consumer_type"`
		ConsumerID   string `json:"consumer_id"`
	}
	if err := c.BodyParser(&req); err != nil {
		return fiber.ErrBadRequest
	}
	if req.ConsumerType == "" || req.ConsumerID == "" {
		return fiber.NewError(fiber.StatusBadRequest, "consumer_type and consumer_id are required")
	}
	if err := s.credentialManager.Bind(c.Context(), id, req.ConsumerType, req.ConsumerID, actorFromCtx(c)); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"status": "bound"})
}

func (s *Server) handleUnbindCredential(c *fiber.Ctx) error {
	id, err := parseCredID(c)
	if err != nil {
		return err
	}
	if err := s.credentialManager.Unbind(c.Context(), id,
		unescapeParam(c, "consumer_type"), unescapeParam(c, "consumer_id"), actorFromCtx(c)); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func (s *Server) handleListCredentialsByConsumer(c *fiber.Ctx) error {
	consumerType := unescapeParam(c, "consumer_type")
	consumerID := unescapeParam(c, "consumer_id")
	if consumerType == "" || consumerID == "" {
		return fiber.NewError(fiber.StatusBadRequest, "consumer_type and consumer_id are required")
	}
	bindings, err := s.credentialManager.ListBindingsByConsumer(c.Context(), consumerType, consumerID)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}
	if bindings == nil {
		bindings = []domain.CredentialBinding{}
	}
	return c.JSON(bindings)
}

// --- helpers ---

func parseCredID(c *fiber.Ctx) (int64, error) {
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		return 0, fiber.ErrBadRequest
	}
	return id, nil
}

func actorFromCtx(c *fiber.Ctx) string {
	if userID, ok := c.Locals("userID").(string); ok {
		return userID
	}
	return ""
}

package dashboard

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"

	"github.com/gofiber/fiber/v2"
	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/modelruntime"
	"github.com/iulita-ai/iulita/internal/models"
)

func (s *Server) registerModelRoutes(api fiber.Router) {
	group := api.Group("/models", auth.AdminOnly(), s.modelAccess)
	group.Get("/catalog", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"models": models.Catalog(), "presets": models.CandidateProfiles()})
	})
	group.Get("/settings", func(c *fiber.Ctx) error {
		return c.JSON(struct {
			modelruntime.View
			LegacySettings         *models.Settings `json:"legacy_settings,omitempty"`
			LegacyClassifierActive bool             `json:"legacy_classifier_active"`
		}{s.modelManager.Snapshot(), s.legacyModelSettings, s.legacyClassifierActive})
	})
	group.Post("/settings/validate", s.handleValidateModelSettings)
	group.Post("/settings/stage", s.handleStageModelSettings)
	group.Get("/history", s.handleModelHistory)
	group.Post("/history/:revision/stage", s.handleStageModelHistory)
	group.Put("/settings", s.handleActivateModelSettings)
	group.Delete("/settings/stage/:id", s.handleDiscardModelStage)
	group.Post("/profiles/:id/probe", s.handleModelProbe)
	group.Get("/probes/:id", s.handleModelProbeStatus)
	group.Post("/probes/:id/cancel", s.handleCancelModelProbe)
	group.Post("/connections/:provider/revoke", s.handleRevokeModelConnection)
}

// Recheck DB privilege and initial password state, not just a potentially old
// JWT role. These endpoints are absent entirely without configured auth.
func (s *Server) modelAccess(c *fiber.Ctx) error {
	claims := auth.GetClaims(c)
	if claims == nil || s.store == nil {
		return modelAPIError(c, &modelruntime.Error{Code: "admin_required", Message: "Administrator access is required", HTTPStatus: 403})
	}
	user, err := s.store.GetUser(c.UserContext(), claims.UserID)
	if err != nil || user == nil || user.Role != domain.RoleAdmin {
		return modelAPIError(c, &modelruntime.Error{Code: "admin_required", Message: "Administrator access is required", HTTPStatus: 403})
	}
	if user.MustChangePass && c.Method() != fiber.MethodGet {
		return modelAPIError(c, &modelruntime.Error{Code: "password_change_required", Message: "Change the initial password before editing or testing models", HTTPStatus: 403})
	}
	return c.Next()
}
func modelAPIError(c *fiber.Ctx, err error) error {
	var coded *modelruntime.Error
	if errors.As(err, &coded) {
		return c.Status(coded.HTTPStatus).JSON(coded)
	}
	return c.Status(503).JSON(fiber.Map{"code": "model_service_unavailable", "message": "Model settings are temporarily unavailable"})
}
func decodeModelBody(c *fiber.Ctx, dst any) error {
	if len(c.Body()) > models.MaxSettingsBytes+8192 {
		return &modelruntime.Error{Code: "payload_too_large", Message: "Model settings exceed the request size limit", HTTPStatus: 413}
	}
	dec := json.NewDecoder(bytes.NewReader(c.Body()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return &modelruntime.Error{Code: "invalid_request", Message: "Check the model settings fields", HTTPStatus: 422}
	}
	var tail any
	if err := dec.Decode(&tail); err != io.EOF {
		return &modelruntime.Error{Code: "invalid_request", Message: "Send one settings document", HTTPStatus: 422}
	}
	return nil
}

type modelDraftRequest struct {
	ExpectedRevision uint64                            `json:"expected_revision"`
	Settings         models.Settings                   `json:"settings"`
	Connections      []modelruntime.ConnectionMutation `json:"connections"`
}

func (s *Server) handleStageModelSettings(c *fiber.Ctx) error {
	var req modelDraftRequest
	if err := decodeModelBody(c, &req); err != nil {
		return modelAPIError(c, err)
	}
	view, err := s.modelManager.Stage(c.UserContext(), auth.GetClaims(c).UserID, req.ExpectedRevision, req.Settings, req.Connections)
	if err != nil {
		return modelAPIError(c, err)
	}
	if s.configStore != nil {
		s.configStore.SetModelPolicyManaged()
	}
	return c.Status(201).JSON(view)
}
func (s *Server) handleValidateModelSettings(c *fiber.Ctx) error {
	var req modelDraftRequest
	if err := decodeModelBody(c, &req); err != nil {
		return modelAPIError(c, err)
	}
	errs := s.modelManager.Validate(c.UserContext(), auth.GetClaims(c).UserID, req.Settings, req.Connections)
	return c.JSON(fiber.Map{"valid": len(errs) == 0, "field_errors": errs})
}
func (s *Server) handleActivateModelSettings(c *fiber.Ctx) error {
	var req struct {
		ExpectedRevision uint64 `json:"expected_revision"`
		StageID          string `json:"stage_id"`
	}
	if err := decodeModelBody(c, &req); err != nil {
		return modelAPIError(c, err)
	}
	view, err := s.modelManager.Activate(c.UserContext(), auth.GetClaims(c).UserID, req.ExpectedRevision, req.StageID)
	if err != nil {
		return modelAPIError(c, err)
	}
	return c.JSON(view)
}
func (s *Server) handleDiscardModelStage(c *fiber.Ctx) error {
	var req struct {
		ExpectedRevision uint64 `json:"expected_revision"`
	}
	if len(c.Body()) > 0 {
		if err := decodeModelBody(c, &req); err != nil {
			return modelAPIError(c, err)
		}
	} else {
		v, err := strconv.ParseUint(c.Query("expected_revision"), 10, 64)
		if err != nil {
			return modelAPIError(c, &modelruntime.Error{Code: "invalid_request", Message: "Include the current settings revision", HTTPStatus: 422})
		}
		req.ExpectedRevision = v
	}
	if err := s.modelManager.Discard(c.UserContext(), auth.GetClaims(c).UserID, req.ExpectedRevision, c.Params("id")); err != nil {
		return modelAPIError(c, err)
	}
	return c.SendStatus(204)
}
func (s *Server) handleModelProbe(c *fiber.Ctx) error {
	var req modelruntime.ProbeRequest
	if err := decodeModelBody(c, &req); err != nil {
		return modelAPIError(c, err)
	}
	if req.ProfileID != "" && req.ProfileID != c.Params("id") {
		return modelAPIError(c, &modelruntime.Error{Code: "invalid_reference", Message: "Profile references do not match", HTTPStatus: 422})
	}
	req.ProfileID = c.Params("id")
	view, err := s.modelManager.Probe(c.UserContext(), auth.GetClaims(c).UserID, req)
	if err != nil {
		return modelAPIError(c, err)
	}
	return c.Status(202).JSON(view)
}
func (s *Server) handleModelProbeStatus(c *fiber.Ctx) error {
	view, err := s.modelManager.ProbeStatus(c.UserContext(), auth.GetClaims(c).UserID, c.Params("id"))
	if err != nil {
		return modelAPIError(c, err)
	}
	return c.JSON(view)
}
func (s *Server) handleCancelModelProbe(c *fiber.Ctx) error {
	view, err := s.modelManager.CancelProbe(c.UserContext(), auth.GetClaims(c).UserID, c.Params("id"))
	if err != nil {
		return modelAPIError(c, err)
	}
	return c.JSON(view)
}
func (s *Server) handleRevokeModelConnection(c *fiber.Ctx) error {
	var req struct {
		ExpectedRevision uint64 `json:"expected_revision"`
		Generation       string `json:"generation"`
	}
	if err := decodeModelBody(c, &req); err != nil {
		return modelAPIError(c, err)
	}
	view, err := s.modelManager.Revoke(c.UserContext(), auth.GetClaims(c).UserID, req.ExpectedRevision, c.Params("provider"), req.Generation)
	if err != nil {
		return modelAPIError(c, err)
	}
	return c.JSON(view)
}

func (s *Server) handleModelHistory(c *fiber.Ctx) error {
	history, err := s.modelManager.History(c.UserContext(), auth.GetClaims(c).UserID)
	if err != nil {
		return modelAPIError(c, err)
	}
	return c.JSON(fiber.Map{"history": history})
}
func (s *Server) handleStageModelHistory(c *fiber.Ctx) error {
	var body struct {
		ExpectedRevision uint64 `json:"expected_revision"`
	}
	if err := decodeModelBody(c, &body); err != nil {
		return modelAPIError(c, err)
	}
	revision, err := strconv.ParseUint(c.Params("revision"), 10, 64)
	if err != nil {
		return modelAPIError(c, &modelruntime.Error{Code: "invalid_request", Message: "Use a retained policy revision", HTTPStatus: 422})
	}
	view, err := s.modelManager.StageHistory(c.UserContext(), auth.GetClaims(c).UserID, body.ExpectedRevision, revision)
	if err != nil {
		return modelAPIError(c, err)
	}
	if s.configStore != nil {
		s.configStore.SetModelPolicyManaged()
	}
	return c.Status(201).JSON(view)
}

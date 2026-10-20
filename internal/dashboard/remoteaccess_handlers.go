package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/gofiber/fiber/v2"
	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/remoteaccess"
)

func (s *Server) registerRemoteAccess(api fiber.Router) {
	ra := api.Group("/remote-access", s.remotePrincipal)
	ra.Get("/", s.remoteSnapshot)
	ra.Post("/identities/generate", s.remoteGenerateIdentity)
	ra.Post("/key-operations", s.remoteKeyOperation)
	ra.Get("/key-operations", s.remoteKeyOperations)
	ra.Get("/approvals", s.remoteApprovals)
	ra.Post("/approvals/:id", s.remoteApprove)
	ra.Get("", s.remoteSnapshot)
	ra.Post("/enabled", auth.AdminOnly(), s.remoteEnabled)
	for _, kind := range []string{"servers", "identities", "routes", "grants"} {
		k := kind
		ra.Post("/"+k, func(c *fiber.Ctx) error { return s.remotePut(c, k) })
		ra.Put("/"+k+"/:id", func(c *fiber.Ctx) error { return s.remotePut(c, k) })
	}
	ra.Post("/runbooks", s.remotePutRunbook)
	ra.Put("/runbooks/:id", s.remotePutRunbook)
	ra.Post("/runbooks/execute", s.remoteRunbook)
	ra.Post("/schedules", s.remoteSchedule)
	ra.Post("/operations/preview", s.remoteOperationPreview)
	ra.Post("/operations", s.remoteOperationExecute)
	ra.Post("/terminals/preview", s.remoteTerminalPreview)
	ra.Post("/terminals/ticket", s.remoteTerminalTicket)
	ra.Post("/executions/preview", s.remotePreview)
	ra.Post("/executions", s.remoteExecute)
	ra.Get("/executions/:id", s.remoteExecution)
	ra.Post("/executions/:id/cancel", s.remoteCancel)
	ra.Get("/artifacts/:id", s.remoteArtifact)
}
func remoteScope(c *fiber.Ctx) (remoteaccess.Scope, error) {
	claims := auth.GetClaims(c)
	if claims == nil || claims.UserID == "" {
		return remoteaccess.Scope{}, fiber.ErrUnauthorized
	}
	c.Set("Cache-Control", "no-store")
	c.Set("X-Content-Type-Options", "nosniff")
	return remoteaccess.Scope{OwnerID: claims.UserID, CallerID: "dashboard"}, nil
}
func remoteBody(c *fiber.Ctx, v any) error {
	if len(c.Body()) > 256<<10 {
		return fiber.ErrRequestEntityTooLarge
	}
	d := json.NewDecoder(bytes.NewReader(c.Body()))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		return fiber.NewError(400, "Invalid remote access request")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return fiber.NewError(400, "Invalid remote access request")
	}
	return nil
}
func remoteError(err error) error {
	if errors.Is(err, remoteaccess.ErrDenied) {
		return fiber.NewError(403, "Remote access denied: check ownership, grants, expiry, enabled state and host trust")
	}
	return fiber.NewError(400, "Remote access request failed validation or storage; no automatic retry")
}
func (s *Server) remoteSnapshot(c *fiber.Ctx) error {
	sc, err := remoteScope(c)
	if err != nil {
		return err
	}
	return c.JSON(s.remoteAccess.Snapshot(sc))
}
func (s *Server) remoteEnabled(c *fiber.Ctx) error {
	_, err := remoteScope(c)
	if err != nil {
		return err
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if bodyErr := remoteBody(c, &body); bodyErr != nil {
		return bodyErr
	}
	if err = s.remoteAccess.SetEnabled(body.Enabled); err != nil {
		return remoteError(err)
	}
	if s.registry != nil {
		if body.Enabled {
			s.registry.EnableGroup("remote_access")
		} else {
			s.registry.DisableGroup("remote_access")
		}
	}
	return c.JSON(fiber.Map{"enabled": body.Enabled, "remote_stop_verified": false})
}
func (s *Server) remotePut(c *fiber.Ctx, kind string) error {
	sc, err := remoteScope(c)
	if err != nil {
		return err
	}
	// Catalog writes grant execution authority and establish host trust. They are
	// administrative actions, still owner scoped; admins never read other owners.
	if auth.GetClaims(c).Role != domain.RoleAdmin {
		return fiber.ErrForbidden
	}
	id := c.Params("id")
	switch kind {
	case "servers":
		var x remoteaccess.Server
		if bodyErr := remoteBody(c, &x); bodyErr != nil {
			return bodyErr
		}
		x.ID = id
		out, putErr := s.remoteAccess.PutServer(sc, x)
		if putErr != nil {
			return remoteError(putErr)
		}
		return c.JSON(out)
	case "identities":
		var x struct {
			Identity remoteaccess.Identity `json:"identity"`
			Secret   *remoteaccess.Secret  `json:"secret"`
		}
		if bodyErr := remoteBody(c, &x); bodyErr != nil {
			return bodyErr
		}
		x.Identity.ID = id
		out, putErr := s.remoteAccess.PutIdentity(c.UserContext(), sc, x.Identity, x.Secret)
		if putErr != nil {
			return remoteError(putErr)
		}
		return c.JSON(out)
	case "routes":
		var x remoteaccess.Route
		if bodyErr := remoteBody(c, &x); bodyErr != nil {
			return bodyErr
		}
		x.ID = id
		out, putErr := s.remoteAccess.PutRoute(sc, x)
		if putErr != nil {
			return remoteError(putErr)
		}
		return c.JSON(out)
	case "grants":
		var x remoteaccess.Grant
		if bodyErr := remoteBody(c, &x); bodyErr != nil {
			return bodyErr
		}
		x.ID = id
		out, putErr := s.remoteAccess.PutGrant(sc, x)
		if putErr != nil {
			return remoteError(putErr)
		}
		return c.JSON(out)
	}
	return fiber.ErrNotFound
}
func (s *Server) remotePreview(c *fiber.Ctx) error {
	sc, err := remoteScope(c)
	if err != nil {
		return err
	}
	var x remoteaccess.ExecuteRequest
	if bodyErr := remoteBody(c, &x); bodyErr != nil {
		return bodyErr
	}
	a, err := s.remoteAccess.Preview(sc, x)
	if err != nil {
		return remoteError(err)
	}
	return c.JSON(a)
}
func (s *Server) remoteExecute(c *fiber.Ctx) error {
	sc, err := remoteScope(c)
	if err != nil {
		return err
	}
	var x remoteaccess.ExecuteRequest
	if bodyErr := remoteBody(c, &x); bodyErr != nil {
		return bodyErr
	}
	if x.ExpectedPlanHash == "" || x.ApprovalID == "" {
		return fiber.NewError(400, "Preview and explicitly approve the exact plan first")
	}
	// The authenticated human POST binds consent to the immutable preview hash.
	// No admin bypass and no free-text chat yes/ok path is used.
	result, err := s.remoteAccess.Execute(c.UserContext(), sc, x, func(_ context.Context, a remoteaccess.Approval) (bool, error) {
		return a.PlanHash == x.ExpectedPlanHash, nil
	})
	if err != nil && result.ID == "" {
		return remoteError(err)
	}
	return c.JSON(result)
}
func (s *Server) remoteExecution(c *fiber.Ctx) error {
	sc, err := remoteScope(c)
	if err != nil {
		return err
	}
	x, ok := s.remoteAccess.Snapshot(sc).Executions[c.Params("id")]
	if !ok {
		return fiber.ErrNotFound
	}
	return c.JSON(x)
}
func (s *Server) remoteCancel(c *fiber.Ctx) error {
	sc, err := remoteScope(c)
	if err != nil {
		return err
	}
	if err = s.remoteAccess.Cancel(sc, c.Params("id")); err != nil {
		return remoteError(err)
	}
	return c.JSON(fiber.Map{"state": "cancel_requested", "remote_stop_verified": false})
}
func (s *Server) remoteArtifact(c *fiber.Ctx) error {
	sc, err := remoteScope(c)
	if err != nil {
		return err
	}
	x, err := s.remoteAccess.Artifact(sc, c.Params("id"))
	if err != nil {
		return fiber.ErrNotFound
	}
	return c.JSON(x)
}

// remotePrincipal revalidates SSH access against current account state; an old
// signed JWT is not authority to keep using a disabled or demoted account.
func (s *Server) remotePrincipal(c *fiber.Ctx) error {
	claims := auth.GetClaims(c)
	if claims == nil || claims.UserID == "" || s.store == nil {
		return fiber.ErrUnauthorized
	}
	user, err := s.store.GetUser(c.UserContext(), claims.UserID)
	if err != nil || user == nil || user.Disabled || user.MustChangePass {
		return fiber.ErrUnauthorized
	}
	if claims.Role == domain.RoleAdmin && user.Role != domain.RoleAdmin {
		claims.Role = user.Role
	}
	return c.Next()
}

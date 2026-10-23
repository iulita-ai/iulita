package dashboard

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/mcpclient"
)

func mcpScope(c *fiber.Ctx) (mcpclient.Scope, error) {
	claims := auth.GetClaims(c)
	if claims == nil || claims.UserID == "" {
		return mcpclient.Scope{}, fiber.ErrUnauthorized
	}
	c.Set("Cache-Control", "no-store")
	c.Set("X-Content-Type-Options", "nosniff")
	return mcpclient.Scope{OwnerID: strings.Clone(claims.UserID), CallerID: "dashboard", Private: true}, nil
}
func mcpError(e error) error {
	switch {
	case e == nil:
		return nil
	case errors.Is(e, mcpclient.ErrDenied):
		return fiber.NewError(404, "MCP connection or operation not found")
	case errors.Is(e, mcpclient.ErrBusy):
		return fiber.NewError(409, e.Error())
	case errors.Is(e, mcpclient.ErrConflict):
		return fiber.NewError(409, e.Error())
	case errors.Is(e, mcpclient.ErrApproval):
		return fiber.NewError(403, e.Error())
	case errors.Is(e, mcpclient.ErrInvalid):
		return fiber.NewError(400, e.Error())
	default:
		return fiber.NewError(503, "MCP unavailable; check settings and connection status")
	}
}
func mcpBody(c *fiber.Ctx, v any) error {
	if err := remoteBody(c, v); err != nil {
		if errors.Is(err, fiber.ErrRequestEntityTooLarge) {
			return fiber.ErrRequestEntityTooLarge
		}
		return fiber.NewError(400, "Invalid MCP request")
	}
	return nil
}
func (s *Server) registerMCP(api fiber.Router) {
	m := api.Group("/mcp")
	m.Get("", s.mcpSnapshot)
	m.Get("/policy", s.mcpPolicy)
	m.Post("/policy", auth.AdminOnly(), s.mcpPolicyUpdate)
	m.Post("/connections", s.mcpCreate)
	m.Get("/connections/:id", s.mcpConnection)
	m.Put("/connections/:id", s.mcpUpdate)
	m.Post("/connections/:id/actions", s.mcpAction)
	m.Post("/connections/:id/credentials", s.mcpCredentials)
	m.Post("/connections/:id/review", s.mcpReview)
	m.Post("/connections/:id/oauth", s.mcpOAuthStart)
	m.Get("/audit", s.mcpAudit)
	m.Get("/plans", s.mcpPlans)
	m.Post("/plans", s.mcpPrepare)
	m.Post("/plans/:id/decision", s.mcpDecision)
	m.Post("/executions/:id/cancel", s.mcpCancel)
	m.Get("/artifacts/:id", s.mcpArtifact)
}

func (s *Server) mcpUpdate(c *fiber.Ctx) error {
	sc, e := mcpScope(c)
	if e != nil {
		return e
	}
	var in struct {
		Revision int64           `json:"revision"`
		Draft    mcpclient.Draft `json:"draft"`
	}
	if requestErr := mcpBody(c, &in); requestErr != nil {
		return requestErr
	}
	v, e := s.mcp.Update(c.UserContext(), sc, c.Params("id"), in.Revision, in.Draft)
	if e != nil {
		return mcpError(e)
	}
	return c.JSON(v)
}
func (s *Server) mcpSnapshot(c *fiber.Ctx) error {
	sc, e := mcpScope(c)
	if e != nil {
		return e
	}
	v, e := s.mcp.Snapshot(c.UserContext(), sc)
	if e != nil {
		return mcpError(e)
	}
	return c.JSON(v)
}
func (s *Server) mcpPolicy(c *fiber.Ctx) error {
	if _, e := mcpScope(c); e != nil {
		return e
	}
	p := s.mcp.Policy()
	return c.JSON(fiber.Map{"enabled": p.Enabled, "revision": p.Revision})
}
func (s *Server) mcpPolicyUpdate(c *fiber.Ctx) error {
	if _, e := mcpScope(c); e != nil {
		return e
	}
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if requestErr := mcpBody(c, &in); requestErr != nil {
		return requestErr
	}
	sc, scopeErr := mcpScope(c)
	if scopeErr != nil {
		return scopeErr
	}
	if requestErr := s.mcp.SetEnabledBy(in.Enabled, sc.OwnerID); requestErr != nil {
		return mcpError(requestErr)
	}
	return c.JSON(s.mcp.Policy())
}
func (s *Server) mcpCreate(c *fiber.Ctx) error {
	sc, e := mcpScope(c)
	if e != nil {
		return e
	}
	var in mcpclient.Draft
	if requestErr := mcpBody(c, &in); requestErr != nil {
		return requestErr
	}
	v, e := s.mcp.Create(c.UserContext(), sc, in)
	if e != nil {
		return mcpError(e)
	}
	return c.Status(201).JSON(v)
}
func (s *Server) mcpConnection(c *fiber.Ctx) error {
	sc, e := mcpScope(c)
	if e != nil {
		return e
	}
	v, e := s.mcp.Connection(c.UserContext(), sc, c.Params("id"))
	if e != nil {
		return mcpError(e)
	}
	return c.JSON(v)
}
func (s *Server) mcpAction(c *fiber.Ctx) error {
	sc, e := mcpScope(c)
	if e != nil {
		return e
	}
	var in struct {
		Action   string `json:"action"`
		Revision int64  `json:"revision"`
	}
	if requestErr := mcpBody(c, &in); requestErr != nil {
		return requestErr
	}
	v, e := s.mcp.Action(c.UserContext(), sc, c.Params("id"), in.Action, in.Revision)
	if e != nil {
		return mcpError(e)
	}
	return c.JSON(v)
}
func (s *Server) mcpCredentials(c *fiber.Ctx) error {
	sc, e := mcpScope(c)
	if e != nil {
		return e
	}
	var in struct {
		Revision int64             `json:"revision"`
		Mode     string            `json:"mode"`
		Token    string            `json:"token"`
		Headers  map[string]string `json:"headers"`
	}
	if requestErr := mcpBody(c, &in); requestErr != nil {
		return requestErr
	}
	if requestErr := s.mcp.SetCredentials(c.UserContext(), sc, c.Params("id"), in.Revision, in.Mode, in.Token, in.Headers); requestErr != nil {
		return mcpError(requestErr)
	}
	return c.SendStatus(204)
}
func (s *Server) mcpReview(c *fiber.Ctx) error {
	sc, e := mcpScope(c)
	if e != nil {
		return e
	}
	var in struct {
		Revision int64            `json:"revision"`
		Review   mcpclient.Review `json:"review"`
	}
	if requestErr := mcpBody(c, &in); requestErr != nil {
		return requestErr
	}
	if requestErr := s.mcp.Review(c.UserContext(), sc, c.Params("id"), in.Revision, in.Review); requestErr != nil {
		return mcpError(requestErr)
	}
	return c.SendStatus(204)
}
func (s *Server) mcpPlans(c *fiber.Ctx) error {
	sc, e := mcpScope(c)
	if e != nil {
		return e
	}
	v, e := s.mcp.Plans(c.UserContext(), sc)
	if e != nil {
		return mcpError(e)
	}
	return c.JSON(v)
}
func (s *Server) mcpPrepare(c *fiber.Ctx) error {
	sc, e := mcpScope(c)
	if e != nil {
		return e
	}
	var in mcpclient.Call
	if requestErr := mcpBody(c, &in); requestErr != nil {
		return requestErr
	}
	v, e := s.mcp.Prepare(c.UserContext(), sc, in)
	if e != nil {
		return mcpError(e)
	}
	return c.JSON(v)
}
func (s *Server) mcpDecision(c *fiber.Ctx) error {
	sc, e := mcpScope(c)
	if e != nil {
		return e
	}
	var in struct {
		Hash     string `json:"hash"`
		Approved bool   `json:"approved"`
	}
	if requestErr := mcpBody(c, &in); requestErr != nil {
		return requestErr
	}
	id := strings.Clone(c.Params("id"))
	if requestErr := s.mcp.Approve(c.UserContext(), sc, id, in.Hash, in.Approved); requestErr != nil {
		return mcpError(requestErr)
	}
	if !in.Approved {
		return c.SendStatus(204)
	}
	// Captured scope is already authenticated. Work is application-root bound and
	// journaled by the service, not tied to Fiber's recycled request context.
	if err := s.mcp.Start(context.Background(), sc, id); err != nil {
		return mcpError(err)
	}
	return c.Status(202).JSON(fiber.Map{"execution_id": id, "state": "accepted"})
}
func (s *Server) mcpCancel(c *fiber.Ctx) error {
	sc, e := mcpScope(c)
	if e != nil {
		return e
	}
	if requestErr := s.mcp.Cancel(c.UserContext(), sc, c.Params("id")); requestErr != nil {
		return mcpError(requestErr)
	}
	return c.JSON(fiber.Map{"state": "cancel_requested", "remote_stop_verified": false})
}
func (s *Server) mcpArtifact(c *fiber.Ctx) error {
	sc, e := mcpScope(c)
	if e != nil {
		return e
	}
	v, e := s.mcp.Artifact(c.UserContext(), sc, c.Params("id"))
	if e != nil {
		return mcpError(e)
	}
	c.Set("Content-Disposition", "attachment; filename=\"mcp-result.json\"")
	return c.JSON(v.Payload)
}
func (s *Server) mcpOAuthStart(c *fiber.Ctx) error {
	sc, e := mcpScope(c)
	if e != nil {
		return e
	}
	var in mcpclient.OAuthStart
	if requestErr := mcpBody(c, &in); requestErr != nil {
		return requestErr
	}
	if s.mcpOAuthRedirect == "" {
		return fiber.NewError(503, "OAuth callback is not configured")
	}
	cookie := mcpOAuthCookie()
	c.Cookie(&fiber.Cookie{Name: "__Host-iulita-mcp-oauth", Value: cookie, Path: "/", HTTPOnly: true, Secure: true, SameSite: "Lax", Expires: time.Now().Add(10 * time.Minute)})
	target, e := s.mcp.BeginOAuth(c.UserContext(), sc, c.Params("id"), in, s.mcpOAuthRedirect, cookie)
	if e != nil {
		return mcpError(e)
	}
	return c.JSON(fiber.Map{"url": target})
}
func (s *Server) mcpOAuthCallback(c *fiber.Ctx) error {
	c.Set("Cache-Control", "no-store")
	c.Set("Referrer-Policy", "no-referrer")
	c.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	if requestErr := s.mcp.FinishOAuth(c.UserContext(), c.Query("state"), c.Query("code"), c.Query("iss"), c.Cookies("__Host-iulita-mcp-oauth")); requestErr != nil {
		return mcpError(requestErr)
	}
	return c.Redirect("/mcp", 303)
}

func mcpOAuthCookie() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *Server) mcpAudit(c *fiber.Ctx) error {
	sc, e := mcpScope(c)
	if e != nil {
		return e
	}
	v, e := s.mcp.Audit(c.UserContext(), sc)
	if e != nil {
		return mcpError(e)
	}
	return c.JSON(v)
}

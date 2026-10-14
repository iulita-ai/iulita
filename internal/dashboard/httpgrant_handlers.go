package dashboard

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/domain"
)

// httpGrantView is the safe API projection of an origin grant — never the
// credential value, and the port/scheme/host are structural (not secrets).
type httpGrantView struct {
	ID            int64      `json:"id"`
	CredentialID  int64      `json:"credential_id"`
	PrincipalType string     `json:"principal_type"`
	PrincipalID   string     `json:"principal_id,omitempty"`
	CallerID      string     `json:"caller_id,omitempty"`
	Scheme        string     `json:"scheme"`
	Host          string     `json:"host"`
	Port          string     `json:"port"`
	Usages        []string   `json:"usages"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	CreatedBy     string     `json:"created_by"`
}

func grantView(g domain.HTTPCredentialGrant) httpGrantView {
	var usages []string
	if err := json.Unmarshal([]byte(g.Usages), &usages); err != nil {
		usages = []string{}
	}
	return httpGrantView{
		ID: g.ID, CredentialID: g.CredentialID,
		PrincipalType: g.PrincipalType, PrincipalID: g.PrincipalID, CallerID: g.CallerID,
		Scheme: g.Scheme, Host: g.Host, Port: g.Port, Usages: usages,
		ExpiresAt: g.ExpiresAt, CreatedAt: g.CreatedAt, CreatedBy: g.CreatedBy,
	}
}

// handleListHTTPGrants returns the origin grants of one credential (admin).
func (s *Server) handleListHTTPGrants(c *fiber.Ctx) error {
	credID, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		return fiber.ErrBadRequest
	}
	grants, err := s.credentialManager.ListHTTPGrants(c.Context(), credID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "listing grants failed"})
	}
	out := make([]httpGrantView, 0, len(grants))
	for i := range grants {
		out = append(out, grantView(grants[i]))
	}
	return c.JSON(out)
}

// handleCreateHTTPGrant publishes an origin grant (admin). Every field is
// validated: closed principal types, http(s) schemes, explicit port, and a
// bounded usage list — the grant IS the authorization, so it is strict.
func (s *Server) handleCreateHTTPGrant(c *fiber.Ctx) error {
	claims := auth.GetClaims(c)
	if claims == nil || claims.Role != domain.RoleAdmin {
		return fiber.ErrForbidden
	}
	credID, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		return fiber.ErrBadRequest
	}
	if _, gerr := s.credentialManager.GetByID(c.Context(), credID); gerr != nil {
		return fiber.ErrNotFound
	}

	var req struct {
		PrincipalType string     `json:"principal_type"`
		PrincipalID   string     `json:"principal_id"`
		CallerID      string     `json:"caller_id"`
		Scheme        string     `json:"scheme"`
		Host          string     `json:"host"`
		Port          string     `json:"port"`
		Usages        []string   `json:"usages"`
		ExpiresAt     *time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}

	switch req.PrincipalType {
	case domain.HTTPGrantPrincipalUser:
		if strings.TrimSpace(req.PrincipalID) == "" {
			return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{"error": "user grants require principal_id"})
		}
	case domain.HTTPGrantPrincipalGlobal:
		// Publishing a global credential is exactly the dangerous operation
		// this UI gates: require an explicit caller binding.
		if strings.TrimSpace(req.CallerID) == "" {
			return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{
				"error": "global grants must bind a caller_id (e.g. http_request) — unrestricted global publication is rejected"})
		}
	default:
		return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{"error": "principal_type must be user or global"})
	}
	req.Scheme = strings.ToLower(strings.TrimSpace(req.Scheme))
	if req.Scheme != "http" && req.Scheme != "https" {
		return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{"error": "scheme must be http or https"})
	}
	req.Host = strings.ToLower(strings.TrimSpace(req.Host))
	if req.Host == "" || strings.ContainsAny(req.Host, " /\\:@") {
		return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{"error": "invalid host"})
	}
	port := strings.TrimSpace(req.Port)
	if port == "" {
		if req.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	if p, perr := strconv.Atoi(port); perr != nil || p < 1 || p > 65535 {
		return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{"error": "invalid port"})
	}
	if len(req.Usages) == 0 {
		req.Usages = []string{"basic", "bearer", "header", "multipart"}
	}
	for _, u := range req.Usages {
		switch u {
		case "basic", "bearer", "header", "multipart":
		default:
			return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{"error": fmt.Sprintf("unknown usage %q", u)})
		}
	}
	if req.ExpiresAt != nil && req.ExpiresAt.Before(time.Now()) {
		return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{"error": "expires_at is in the past"})
	}

	usagesJSON, merr := json.Marshal(req.Usages)
	if merr != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "encoding usages failed"})
	}
	g := &domain.HTTPCredentialGrant{
		CredentialID: credID, PrincipalType: req.PrincipalType, PrincipalID: req.PrincipalID,
		CallerID: strings.TrimSpace(req.CallerID), Scheme: req.Scheme, Host: req.Host, Port: port,
		Usages: string(usagesJSON), ExpiresAt: req.ExpiresAt, CreatedBy: claims.UserID,
	}
	if err := s.credentialManager.SaveHTTPGrant(c.Context(), g); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "saving grant failed"})
	}
	return c.Status(fiber.StatusCreated).JSON(grantView(*g))
}

// handleDeleteHTTPGrant removes a grant (admin).
func (s *Server) handleDeleteHTTPGrant(c *fiber.Ctx) error {
	claims := auth.GetClaims(c)
	if claims == nil || claims.Role != domain.RoleAdmin {
		return fiber.ErrForbidden
	}
	gid, err := strconv.ParseInt(c.Params("gid"), 10, 64)
	if err != nil {
		return fiber.ErrBadRequest
	}
	if err := s.credentialManager.DeleteHTTPGrant(c.Context(), gid); err != nil {
		return fiber.ErrNotFound
	}
	return c.SendStatus(fiber.StatusNoContent)
}

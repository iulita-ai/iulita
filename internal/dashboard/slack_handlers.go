package dashboard

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/domain"
	slackskill "github.com/iulita-ai/iulita/internal/skill/slack"
)

const slackStateCookie = "slack_oauth_state"

// handleSlackAuth starts the owner's personal Slack OAuth flow. Admin-only.
func (s *Server) handleSlackAuth(c *fiber.Ctx) error {
	if s.slackClient == nil || !s.slackClient.Configured() {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "Slack integration not configured (set skills.slack_oauth.client_id and client_secret)",
		})
	}
	// Encryption gate: a personal xoxp- token exposes the owner's whole Slack
	// view, so refuse to even start the flow unless tokens can be encrypted.
	if !s.slackClient.EncryptionEnabled() {
		return c.Status(fiber.StatusPreconditionFailed).JSON(fiber.Map{
			"error": "config encryption must be enabled before connecting Slack — set IULITA_CONFIG_KEY or ensure the encryption key file/keyring is available, then restart",
		})
	}
	claims := auth.GetClaims(c) // non-nil: route is behind AdminOnly()
	if claims == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "not authenticated"})
	}

	// HMAC-signed state binds the initiating owner and is unforgeable, so a forced
	// state cookie cannot be crafted to pass VerifyState on the public callback.
	state, err := s.slackClient.NewSignedState(claims.UserID)
	if err != nil {
		s.logger.Error("failed to generate slack oauth state", zap.Error(err))
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal error"})
	}

	c.Cookie(&fiber.Cookie{
		Name:     slackStateCookie,
		Value:    state,
		Path:     "/api/slack",
		Expires:  time.Now().Add(10 * time.Minute),
		HTTPOnly: true,
		Secure:   c.Protocol() == "https",
		SameSite: "Lax",
	})
	return c.JSON(fiber.Map{"url": s.slackClient.AuthCodeURL(state)})
}

// handleSlackCallback handles Slack's OAuth redirect. Registered publicly (before
// the JWT middleware) because a browser redirect carries no Authorization header.
// It is protected by (a) a double-submit HttpOnly state cookie, (b) an HMAC
// signature over the state (unforgeable — a forced cookie can't be crafted), and
// (c) a DB re-check that the embedded owner is still an admin.
func (s *Server) handleSlackCallback(c *fiber.Ctx) error {
	if s.slackClient == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "Slack integration not configured"})
	}
	// Always clear the state cookie: the flow is single-use regardless of outcome.
	defer s.clearSlackState(c)

	if errParam := c.Query("error"); errParam != "" {
		return c.Redirect("/settings?slack=denied")
	}
	// Defense in depth: never persist a token if it can't be encrypted at rest,
	// even though the flow can only be reached after the /auth encryption gate.
	if !s.slackClient.EncryptionEnabled() {
		s.logger.Error("slack callback reached with encryption disabled")
		return c.Status(fiber.StatusPreconditionFailed).JSON(fiber.Map{"error": "config encryption is not enabled"})
	}
	code := c.Query("code")
	if code == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "missing authorization code"})
	}

	// Double-submit cookie + HMAC verification. The mismatch is the most common
	// symptom of a redirect_url/host mismatch, so log the configured URL to help
	// operators diagnose it.
	stateParam := c.Query("state")
	stateCookie := c.Cookies(slackStateCookie)
	if stateCookie == "" || stateParam != stateCookie {
		s.logger.Warn("slack callback invalid state (often a redirect_url/host mismatch)",
			zap.String("configured_redirect_url", s.slackClient.RedirectURL()))
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid state parameter"})
	}
	ownerUserID, ok := s.slackClient.VerifyState(stateParam)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid state signature"})
	}

	// Owner re-check: the signed id must still resolve to an admin.
	owner, err := s.store.GetUser(c.Context(), ownerUserID)
	if err != nil || owner == nil || owner.Role != domain.RoleAdmin {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "Slack connection is restricted to the account owner"})
	}

	result, err := s.slackClient.ExchangeCode(c.Context(), code)
	if err != nil {
		s.logger.Error("slack oauth exchange failed", zap.Error(err))
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "failed to exchange authorization code"})
	}

	// Fail closed if Slack granted any write scope — the personal token must be
	// provably read-only even if the app is misconfigured with extra scopes.
	if slackskill.HasWriteScope(result.Scope) {
		s.logger.Error("slack oauth returned a write scope; refusing to connect", zap.String("scopes", result.Scope))
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "Slack returned write scopes; only read access is allowed"})
	}

	// Single-owner: there may be at most one connected account total. Reject a
	// second user, and reject a silent workspace switch for the same user.
	existing, err := s.store.GetAnySlackAccount(c.Context())
	if err != nil {
		s.logger.Error("slack account lookup failed", zap.Error(err))
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal error"})
	}
	if existing != nil {
		if existing.UserID != ownerUserID {
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "another user has already connected Slack; disconnect it first"})
		}
		if existing.TeamID != result.TeamID {
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "a different Slack workspace is already connected; disconnect it first"})
		}
	}

	encAccess, err := s.slackClient.EncryptToken(result.AccessToken)
	if err != nil {
		s.logger.Error("encrypting slack access token failed", zap.Error(err))
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal error"})
	}
	encRefresh, err := s.slackClient.EncryptToken(result.RefreshToken)
	if err != nil {
		s.logger.Error("encrypting slack refresh token failed", zap.Error(err))
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal error"})
	}
	scopesJSON, err := json.Marshal(splitScopes(result.Scope))
	if err != nil {
		scopesJSON = []byte("[]")
	}

	// Delete + save so a reconnect refreshes ALL fields (scopes, slack user id,
	// tokens), not just the token columns.
	if err := s.store.DeleteSlackAccount(c.Context(), ownerUserID); err != nil {
		s.logger.Error("clearing prior slack account failed", zap.Error(err))
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal error"})
	}
	account := &domain.SlackAccount{
		UserID:                ownerUserID,
		SlackUserID:           result.AuthedUserID,
		TeamID:                result.TeamID,
		TeamName:              result.TeamName,
		EncryptedAccessToken:  encAccess,
		EncryptedRefreshToken: encRefresh,
		TokenExpiry:           result.Expiry,
		Scopes:                string(scopesJSON),
	}
	if err := s.store.SaveSlackAccount(c.Context(), account); err != nil {
		s.logger.Error("saving slack account failed", zap.Error(err))
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal error"})
	}
	// Enable the slack_search skill immediately (no restart) now that an account
	// is connected.
	if s.registry != nil {
		s.registry.AddCapability("slack_user")
	}
	return c.Redirect("/settings?slack=connected")
}

// handleSlackStatus reports the owner's Slack connection status. Admin-only.
func (s *Server) handleSlackStatus(c *fiber.Ctx) error {
	userID := getUserID(c)
	if userID == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "not authenticated"})
	}
	// Slack is single-owner: resolve THE connected account, not the caller's, so
	// any admin sees the true connection status.
	account, err := s.store.GetAnySlackAccount(c.Context())
	if err != nil {
		s.logger.Error("slack status lookup failed", zap.Error(err))
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal error"})
	}
	if account == nil {
		return c.JSON(fiber.Map{"source": "none"})
	}
	var scopes []string
	if err := json.Unmarshal([]byte(account.Scopes), &scopes); err != nil {
		scopes = []string{}
	}
	return c.JSON(fiber.Map{
		"source":        "oauth",
		"team_id":       account.TeamID,
		"team_name":     account.TeamName,
		"slack_user_id": account.SlackUserID,
		"scopes":        scopes,
		"connected_at":  account.CreatedAt,
	})
}

// handleDeleteSlackAccount disconnects the (single-owner) Slack account. Admin-only.
func (s *Server) handleDeleteSlackAccount(c *fiber.Ctx) error {
	if getUserID(c) == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "not authenticated"})
	}
	// Resolve the single connected account so any admin can disconnect it (and so
	// we only disable the skill when something was actually removed).
	account, err := s.store.GetAnySlackAccount(c.Context())
	if err != nil {
		s.logger.Error("slack disconnect lookup failed", zap.Error(err))
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal error"})
	}
	if account == nil {
		return c.JSON(fiber.Map{"status": "disconnected"})
	}
	if err := s.store.DeleteSlackAccount(c.Context(), account.UserID); err != nil {
		s.logger.Error("slack disconnect failed", zap.Error(err))
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal error"})
	}
	// Disable the slack_search skill immediately so a revoked account can't be used.
	if s.registry != nil {
		s.registry.RemoveCapability("slack_user")
	}
	return c.JSON(fiber.Map{"status": "disconnected"})
}

func (s *Server) clearSlackState(c *fiber.Ctx) {
	c.Cookie(&fiber.Cookie{
		Name:     slackStateCookie,
		Value:    "",
		Expires:  time.Now().Add(-time.Hour),
		HTTPOnly: true,
		Secure:   c.Protocol() == "https",
		SameSite: "Lax",
	})
}

// splitScopes turns Slack's comma-separated scope string into a slice, dropping
// empties.
func splitScopes(scope string) []string {
	parts := strings.Split(scope, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

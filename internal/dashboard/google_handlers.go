package dashboard

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/domain"
)

// getUserID extracts the user ID from JWT claims in fiber context.
func getUserID(c *fiber.Ctx) string {
	claims := auth.GetClaims(c)
	if claims == nil {
		return ""
	}
	return claims.UserID
}

// handleGoogleAuth starts the OAuth2 flow by redirecting to Google's consent screen.
func (s *Server) handleGoogleAuth(c *fiber.Ctx) error {
	if s.googleClient == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "Google integration not configured"})
	}

	state, err := generateState()
	if err != nil {
		s.logger.Error("failed to generate oauth state", zap.Error(err))
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal error"})
	}
	c.Cookie(&fiber.Cookie{
		Name:     "google_oauth_state",
		Value:    state,
		Expires:  time.Now().Add(10 * time.Minute),
		HTTPOnly: true,
		Secure:   c.Protocol() == "https",
		SameSite: "Lax",
	})

	accountAlias := c.Query("account_alias", "")
	if accountAlias != "" {
		state = state + ":" + accountAlias
	}

	url := s.googleClient.AuthCodeURL(state)
	return c.JSON(fiber.Map{"url": url})
}

// handleGoogleCallback handles the OAuth2 callback from Google.
func (s *Server) handleGoogleCallback(c *fiber.Ctx) error {
	if s.googleClient == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "Google integration not configured"})
	}

	userID := getUserID(c)
	if userID == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "not authenticated"})
	}

	code := c.Query("code")
	if code == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "missing authorization code"})
	}

	stateParam := c.Query("state")
	stateCookie := c.Cookies("google_oauth_state")
	if stateCookie == "" || (stateParam != stateCookie && !hasStatePrefix(stateParam, stateCookie)) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid state parameter"})
	}

	alias := ""
	if len(stateParam) > len(stateCookie)+1 {
		alias = stateParam[len(stateCookie)+1:]
	}

	accessToken, refreshToken, expiry, err := s.googleClient.ExchangeCodeRaw(c.Context(), code)
	if err != nil {
		s.logger.Error("google oauth exchange failed", zap.Error(err))
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "failed to exchange authorization code"})
	}

	email, err := fetchGoogleEmail(c.Context(), accessToken)
	if err != nil {
		s.logger.Error("failed to fetch google email", zap.Error(err))
		email = "unknown@google.com"
	}

	encAccess, err := s.googleClient.EncryptToken(accessToken)
	if err != nil {
		return s.errorResponse(c, err)
	}
	encRefresh, err := s.googleClient.EncryptToken(refreshToken)
	if err != nil {
		return s.errorResponse(c, err)
	}

	scopes, _ := json.Marshal([]string{
		"gmail.readonly", "calendar.readonly", "contacts.readonly", "tasks",
	})

	account := &domain.GoogleAccount{
		UserID:                userID,
		AccountEmail:          email,
		AccountAlias:          alias,
		EncryptedAccessToken:  encAccess,
		EncryptedRefreshToken: encRefresh,
		TokenExpiry:           expiry,
		Scopes:                string(scopes),
	}

	existing, err := s.store.GetGoogleAccountByEmail(c.Context(), userID, email)
	if err == nil && existing != nil {
		if err := s.store.UpdateGoogleTokens(c.Context(), existing.ID, encAccess, encRefresh, expiry); err != nil {
			return s.errorResponse(c, err)
		}
		s.logger.Info("google account reconnected", zap.String("email", email), zap.String("user_id", userID))
	} else {
		accounts, _ := s.store.ListGoogleAccounts(c.Context(), userID)
		if len(accounts) == 0 {
			account.IsDefault = true
		}
		if err := s.store.SaveGoogleAccount(c.Context(), account); err != nil {
			return s.errorResponse(c, err)
		}
		s.logger.Info("google account connected", zap.String("email", email), zap.String("user_id", userID))
	}

	c.Cookie(&fiber.Cookie{
		Name:     "google_oauth_state",
		Value:    "",
		Expires:  time.Now().Add(-1 * time.Hour),
		HTTPOnly: true,
		Secure:   c.Protocol() == "https",
		SameSite: "Lax",
	})

	return c.Redirect("/settings?google=connected")
}

// handleListGoogleAccounts lists connected Google accounts for the current user.
func (s *Server) handleListGoogleAccounts(c *fiber.Ctx) error {
	userID := getUserID(c)
	if userID == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "not authenticated"})
	}

	accounts, err := s.store.ListGoogleAccounts(c.Context(), userID)
	if err != nil {
		return s.errorResponse(c, err)
	}

	type accountResponse struct {
		ID           int64     `json:"id"`
		AccountEmail string    `json:"account_email"`
		AccountAlias string    `json:"account_alias"`
		IsDefault    bool      `json:"is_default"`
		TokenExpiry  time.Time `json:"token_expiry"`
		Scopes       string    `json:"scopes"`
		CreatedAt    time.Time `json:"created_at"`
	}

	result := make([]accountResponse, len(accounts))
	for i, a := range accounts {
		result[i] = accountResponse{
			ID:           a.ID,
			AccountEmail: a.AccountEmail,
			AccountAlias: a.AccountAlias,
			IsDefault:    a.IsDefault,
			TokenExpiry:  a.TokenExpiry,
			Scopes:       a.Scopes,
			CreatedAt:    a.CreatedAt,
		}
	}

	return c.JSON(result)
}

// handleDeleteGoogleAccount disconnects a Google account.
func (s *Server) handleDeleteGoogleAccount(c *fiber.Ctx) error {
	userID := getUserID(c)
	if userID == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "not authenticated"})
	}

	id, err := c.ParamsInt("id")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}

	account, err := s.store.GetGoogleAccount(c.Context(), int64(id))
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "account not found"})
	}
	if account.UserID != userID {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "not your account"})
	}

	if err := s.store.DeleteGoogleAccount(c.Context(), int64(id)); err != nil {
		return s.errorResponse(c, err)
	}

	s.logger.Info("google account disconnected", zap.String("email", account.AccountEmail), zap.String("user_id", userID))
	return c.JSON(fiber.Map{"status": "disconnected"})
}

// handleUpdateGoogleAccount updates alias or default status.
func (s *Server) handleUpdateGoogleAccount(c *fiber.Ctx) error {
	userID := getUserID(c)
	if userID == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "not authenticated"})
	}

	id, err := c.ParamsInt("id")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}

	account, err := s.store.GetGoogleAccount(c.Context(), int64(id))
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "account not found"})
	}
	if account.UserID != userID {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "not your account"})
	}

	var body struct {
		Alias     *string `json:"account_alias"`
		IsDefault *bool   `json:"is_default"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}

	alias := account.AccountAlias
	isDefault := account.IsDefault
	if body.Alias != nil {
		alias = *body.Alias
	}
	if body.IsDefault != nil {
		isDefault = *body.IsDefault
	}

	if err := s.store.UpdateGoogleAccountMeta(c.Context(), account.ID, alias, isDefault); err != nil {
		return s.errorResponse(c, err)
	}

	return c.JSON(fiber.Map{"status": "updated"})
}

// handleGoogleStatus returns the current Google credential resolution status.
func (s *Server) handleGoogleStatus(c *fiber.Ctx) error {
	userID := getUserID(c)

	if sp, ok := s.googleClient.(GoogleStatusProvider); ok {
		status := sp.GetCredentialStatus(c.Context(), userID)
		return c.JSON(status)
	}

	// Fallback: minimal status based on config store.
	result := fiber.Map{
		"source":        "unknown",
		"active_scopes": "",
	}

	if s.configStore != nil {
		if v, ok := s.configStore.GetEffective("skills.google.credentials_file"); ok && v != "" {
			result["source"] = "config_credentials_file"
			result["file_path"] = v
		} else if v, ok := s.configStore.GetEffective("skills.google.client_id"); ok && v != "" {
			result["source"] = "oauth2_configured"
		}
		if v, ok := s.configStore.GetEffective("skills.google.scopes"); ok && v != "" {
			result["active_scopes"] = v
		}
	}

	return c.JSON(result)
}

// handleUploadGoogleCredentials accepts a multipart file upload of a Google credentials JSON.
func (s *Server) handleUploadGoogleCredentials(c *fiber.Ctx) error {
	userID := getUserID(c)
	if userID == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "not authenticated"})
	}

	// Admin only.
	claims := auth.GetClaims(c)
	if claims == nil || claims.Role != "admin" {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "admin access required"})
	}

	uploader, ok := s.googleClient.(GoogleCredentialUploader)
	if !ok {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "Google credential upload not supported"})
	}

	file, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "no file uploaded"})
	}

	if file.Size > 1<<20 { // 1MB limit
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "file too large (max 1MB)"})
	}

	f, err := file.Open()
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "failed to read uploaded file"})
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "failed to read uploaded file"})
	}

	paths := config.ResolvePaths()
	credType, destPath, err := uploader.UploadCredentials(data, file.Filename, paths.DataDir)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}

	// Persist in config store.
	if s.configStore != nil {
		if err := s.configStore.Set(c.Context(), "skills.google.credentials_file", destPath, userID, false); err != nil {
			s.logger.Error("failed to persist credentials_file config", zap.Error(err))
		}
	}

	return c.JSON(fiber.Map{
		"status":          "uploaded",
		"credential_type": credType,
		"file_path":       destPath,
		"filename":        file.Filename,
	})
}

func generateState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating state: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func hasStatePrefix(full, prefix string) bool {
	return len(full) >= len(prefix) && full[:len(prefix)] == prefix
}

func fetchGoogleEmail(ctx context.Context, accessToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.googleapis.com/oauth2/v2/userinfo", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return "", err
	}

	var info struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return "", fmt.Errorf("parsing userinfo: %w", err)
	}

	if info.Email == "" {
		return "", fmt.Errorf("no email in userinfo response")
	}
	return info.Email, nil
}

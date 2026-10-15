package browseroauth

import (
	"context"
	"encoding/json"
	"mime"
	"net/http"
	"net/url"
	"time"

	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
	"github.com/ory/fosite"
)

func (s *Server) oauthError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if writeErr := json.NewEncoder(w).Encode(map[string]string{"error": code}); writeErr != nil {
		return
	}
}
func (s *Server) tokenLimit(w http.ResponseWriter, r *http.Request, category string) bool {
	ok, err := s.config.Store.AllowBrowserAttempt(r.Context(), category, ip(r), 60, 10*time.Minute)
	if err != nil {
		s.oauthError(w, 503, "temporarily_unavailable")
		return false
	}
	if !ok {
		w.Header().Set("Retry-After", "600")
		s.oauthError(w, 429, "temporarily_unavailable")
		return false
	}
	return true
}
func (s *Server) tokenForm(w http.ResponseWriter, r *http.Request, allowed []string) (url.Values, bool) {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/x-www-form-urlencoded" || r.Method != "POST" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "" {
		s.oauthError(w, 400, "invalid_request")
		return nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if r.ParseForm() != nil || !one(r.PostForm, allowed) {
		s.oauthError(w, 400, "invalid_request")
		return nil, false
	}
	return r.PostForm, true
}
func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	f, ok := s.tokenForm(w, r, []string{"grant_type", "client_id", "code", "redirect_uri", "code_verifier", "refresh_token", "iulita_installation_jwk"})
	if !ok {
		return
	}
	if !s.tokenLimit(w, r, "token.ip") {
		return
	}
	grant := f.Get("grant_type")
	raw := f.Get("code")
	switch grant {
	case "refresh_token":
		raw = f.Get("refresh_token")
		if f.Has("code") || f.Has("redirect_uri") || f.Has("code_verifier") || f.Has("iulita_installation_jwk") {
			s.oauthError(w, 400, "invalid_request")
			return
		}
	case "authorization_code":
		if f.Has("refresh_token") {
			s.oauthError(w, 400, "invalid_request")
			return
		}
	default:
		s.oauthError(w, 400, "unsupported_grant_type")
		return
	}
	var key oauthstore.InstallationJWK
	if grant == "authorization_code" {
		var err error
		key, err = oauthstore.ParseInstallation([]byte(f.Get("iulita_installation_jwk")))
		if err != nil {
			s.oauthError(w, 400, "invalid_request")
			return
		}
	}
	owner, err := s.config.Store.TokenOwner(r.Context(), grant, raw, f.Get("client_id"), f.Get("redirect_uri"))
	if err != nil {
		s.oauthError(w, 400, "invalid_grant")
		return
	}
	var response fosite.AccessResponder
	err = s.config.Accounts.Guard(r.Context(), owner, func(a Account) error {
		if a.ID != owner {
			return fosite.ErrInvalidGrant
		}
		if stepErr1 := s.config.Store.SyncAccount(r.Context(), owner, a.CredentialVersion, available(a)); stepErr1 != nil {
			return stepErr1
		}
		if !available(a) {
			return fosite.ErrInvalidGrant
		}
		return s.config.Store.TokenRequest(r.Context(), grant, raw, f.Get("client_id"), f.Get("redirect_uri"), s.strategy, func(ctx context.Context) error {
			ar, stepErr2 := s.provider.NewAccessRequest(ctx, r.Clone(ctx), new(oauthstore.Session))
			if stepErr2 != nil {
				return stepErr2
			}
			if grant == "authorization_code" {
				if checkErr3 := s.config.Store.BindInstallation(ctx, ar, key); checkErr3 != nil {
					return checkErr3
				}
			} else {
				if checkErr4 := s.config.Store.FinalizeRefresh(ctx, ar); checkErr4 != nil {
					return checkErr4
				}
			}
			response, stepErr2 = s.provider.NewAccessResponse(ctx, ar)
			if stepErr2 != nil {
				return stepErr2
			}
			session, validSession := ar.GetSession().(*oauthstore.Session)
			if !validSession || session == nil {
				return fosite.ErrInvalidGrant
			}
			stage := session.Binding.Stage
			response.SetExtra("iulita_token_stage", string(stage))
			challenge, stepErr2 := s.config.Store.Challenge(ctx, ar)
			if stepErr2 != nil {
				return stepErr2
			}
			if challenge != nil {
				response.SetExtra("iulita_challenge", challenge)
			}
			return nil
		})
	})
	if err != nil {
		s.oauthError(w, 400, "invalid_grant")
		return
	}
	// No token is written until code consumption, rotation, proof finalization,
	// access/refresh persistence and challenge creation commit together.
	s.provider.WriteAccessResponse(r.Context(), w, nil, response)
}
func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	f, ok := s.tokenForm(w, r, []string{"client_id", "token", "token_type_hint"})
	if !ok {
		return
	}
	if !s.tokenLimit(w, r, "revoke.ip") {
		return
	}
	if f.Get("token") == "" || f.Get("client_id") == "" {
		s.oauthError(w, 400, "invalid_request")
		return
	}
	owner, lookupErr := s.config.Store.TokenOwner(r.Context(), "refresh_token", f.Get("token"), f.Get("client_id"), "")
	if lookupErr != nil {
		if claims, err := s.config.Store.VerifySignature(f.Get("token")); err == nil && claims.ClientID == f.Get("client_id") {
			owner = claims.Subject
		}
	}
	var revokeErr error
	if owner != "" {
		revokeErr = s.config.Accounts.Guard(r.Context(), owner, func(a Account) error {
			return s.config.Store.RevokeCredential(r.Context(), f.Get("token"), f.Get("client_id"), f.Get("token_type_hint"))
		})
	}
	if revokeErr != nil {
		s.oauthError(w, 400, "invalid_request")
		return
	}
	w.WriteHeader(200)
}

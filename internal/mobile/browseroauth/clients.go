package browseroauth

import (
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
)

func (s *Server) reply(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		return
	}
}
func (s *Server) deviceAuthorization(w http.ResponseWriter, r *http.Request) {
	f, ok := s.tokenForm(w, r, []string{"client_id", "scope", "iulita_installation_key_thumbprint"})
	if !ok {
		return
	}
	if r.Header.Get("Authorization") != "" {
		s.oauthError(w, 400, "invalid_request")
		return
	}
	if !s.tokenLimit(w, r, "device.start.ip") {
		return
	}
	reply, err := s.config.Store.CreateDeviceAuthorization(r.Context(), f.Get("client_id"), f.Get("scope"), f.Get("iulita_installation_key_thumbprint"))
	if err != nil {
		s.oauthError(w, 400, "unauthorized_client")
		return
	}
	s.reply(w, reply)
}

var devicePage = template.Must(template.New("device").Parse(`<!doctype html><html lang="ru"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Вход из консоли</title><main><h1>Вход из консоли</h1><p>Продолжайте только если вы сами начали вход. Введите код из своей консоли. Не используйте код, присланный другим человеком.</p><form method="get" action="/oauth2/device"><label>Код <input name="user_code" required maxlength="9" autocomplete="off"></label><button>Продолжить</button></form></main></html>`))

func (s *Server) deviceVerification(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" || len(r.URL.RawQuery) > 64 {
		s.fail(w, 400, "Неверный запрос.")
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || !one(q, []string{"user_code"}) {
		s.fail(w, 400, "Неверный запрос.")
		return
	}
	if !s.limit(w, r, "device.verify.ip", ip(r), 30) {
		return
	}
	if q.Get("user_code") == "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err = devicePage.Execute(w, nil); err != nil {
			return
		}
		return
	}
	authorization, err := s.config.Store.DeviceBrowserAuthorization(r.Context(), q.Get("user_code"))
	if err != nil {
		s.fail(w, 400, "Код неверен или устарел. Начните вход в своей консоли.")
		return
	}
	cookie := browserCookie(r)
	if cookie == "" {
		cookie, err = oauthstore.Secret()
		if err != nil {
			s.fail(w, 503, "Сервис недоступен.")
			return
		}
	}
	flow, csrf, err := s.config.Store.CreateBrowserFlow(r.Context(), cookie, authorization)
	if err != nil {
		s.fail(w, 400, "Не удалось начать вход.")
		return
	}
	registration, err := s.config.Store.ClientPolicy(r.Context(), authorization.ClientID)
	if err != nil {
		s.fail(w, 400, "Приложение недоступно.")
		return
	}
	setCookie(w, cookie)
	s.page(w, pageData{Flow: flow.ID, CSRF: csrf, App: registration.Name, Login: true})
}
func (s *Server) deviceToken(w http.ResponseWriter, r *http.Request, f url.Values) {
	if r.Header.Get("Authorization") != "" || !one(f, []string{"grant_type", "client_id", "device_code", "iulita_installation_jwk"}) {
		s.oauthError(w, 400, "invalid_request")
		return
	}
	key, err := oauthstore.ParseInstallation([]byte(f.Get("iulita_installation_jwk")))
	if err != nil {
		s.oauthError(w, 400, "invalid_request")
		return
	}
	owner, protocolError, err := s.config.Store.PollDeviceAuthorization(r.Context(), f.Get("client_id"), f.Get("device_code"), key)
	if err != nil {
		s.oauthError(w, 503, "temporarily_unavailable")
		return
	}
	if protocolError != "" {
		s.oauthError(w, 400, protocolError)
		return
	}
	var reply *oauthstore.TokenReply
	err = s.config.Accounts.Guard(r.Context(), owner, func(a Account) error {
		if a.ID != owner {
			return oauthstore.ErrInvalid
		}
		if syncErr := s.config.Store.SyncAccount(r.Context(), owner, a.CredentialVersion, available(a)); syncErr != nil {
			return syncErr
		}
		if !available(a) {
			return oauthstore.ErrInvalid
		}
		var consumeErr error
		reply, consumeErr = s.config.Store.ConsumeDeviceAuthorization(r.Context(), f.Get("client_id"), f.Get("device_code"), owner, key)
		return consumeErr
	})
	if err != nil {
		s.oauthError(w, 400, "invalid_grant")
		return
	}
	s.reply(w, reply)
}
func serviceBasic(r *http.Request, f url.Values) (id, secret string, ok bool) {
	if len(r.Header.Values("Authorization")) != 1 || f.Has("client_id") || f.Has("client_secret") {
		return "", "", false
	}
	id, secret, ok = r.BasicAuth()
	if !ok {
		return "", "", false
	}
	id, err := url.QueryUnescape(id)
	if err != nil {
		return "", "", false
	}
	secret, err = url.QueryUnescape(secret)
	if err != nil {
		return "", "", false
	}
	return id, secret, id != "" && oauthstore.ValidSecret(secret)
}
func (s *Server) invalidClient(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="iulita-oauth"`)
	s.oauthError(w, 401, "invalid_client")
}
func (s *Server) serviceToken(w http.ResponseWriter, r *http.Request, f url.Values) {
	if !one(f, []string{"grant_type", "scope"}) {
		s.oauthError(w, 400, "invalid_request")
		return
	}
	id, secret, ok := serviceBasic(r, f)
	if !ok {
		s.invalidClient(w)
		return
	}
	owner, err := s.config.Store.ServiceClientOwner(r.Context(), id, secret)
	if err != nil {
		s.serviceClientError(w, err)
		return
	}
	var reply *oauthstore.TokenReply
	err = s.config.Accounts.Guard(r.Context(), owner, func(a Account) error {
		if a.ID != owner {
			return oauthstore.ErrInvalid
		}
		if syncErr := s.config.Store.SyncAccount(r.Context(), owner, a.CredentialVersion, available(a)); syncErr != nil {
			return syncErr
		}
		if !available(a) {
			return oauthstore.ErrInvalid
		}
		var issueErr error
		reply, issueErr = s.config.Store.IssueServiceToken(r.Context(), id, secret, strings.TrimSpace(f.Get("scope")))
		return issueErr
	})
	if err != nil {
		switch {
		case errors.Is(err, oauthstore.ErrScope):
			s.oauthError(w, 400, "invalid_scope")
		case errors.Is(err, oauthstore.ErrClientAuthentication):
			s.invalidClient(w)
		case errors.Is(err, oauthstore.ErrInvalid):
			s.oauthError(w, 400, "invalid_grant")
		default:
			w.Header().Set("Retry-After", "60")
			s.oauthError(w, 503, "temporarily_unavailable")
		}
		return
	}
	s.reply(w, reply)
}
func (s *Server) serviceRevoke(w http.ResponseWriter, r *http.Request, f url.Values) {
	id, secret, ok := serviceBasic(r, f)
	if !ok {
		s.invalidClient(w)
		return
	}
	if f.Get("token") == "" {
		s.oauthError(w, 400, "invalid_request")
		return
	}
	owner, err := s.config.Store.ServiceClientOwner(r.Context(), id, secret)
	if err != nil {
		s.serviceClientError(w, err)
		return
	}
	// Match the account -> OAuth reservation used by RPC mutations and async
	// completion. A successful revoke must wait for any already-authorized
	// account transaction to commit, rather than acknowledging before it does.
	// Disabled accounts can still revoke their existing service credentials.
	err = s.config.Accounts.Guard(r.Context(), owner, func(a Account) error {
		if a.ID != owner {
			return oauthstore.ErrInvalid
		}
		return s.config.Store.RevokeServiceAccess(r.Context(), id, secret, f.Get("token"))
	})
	if err != nil {
		s.serviceClientError(w, err)
		return
	}
	w.WriteHeader(200)
}

// Authentication failures are terminal; storage/account-reservation failures
// are retryable and must not tell an operator to replace a valid secret.
func (s *Server) serviceClientError(w http.ResponseWriter, err error) {
	if errors.Is(err, oauthstore.ErrClientAuthentication) {
		s.invalidClient(w)
		return
	}
	w.Header().Set("Retry-After", "60")
	s.oauthError(w, 503, "temporarily_unavailable")
}

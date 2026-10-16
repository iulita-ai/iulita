// Package browseroauth implements the independent browser authorization, token
// and mobile protocol endpoints. Listener lifecycle belongs to the runtime.
package browseroauth

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
	"github.com/iulita-ai/iulita/internal/mobile/serverclock"
	"github.com/ory/fosite"
	"github.com/ory/fosite/compose"
	"github.com/ory/fosite/handler/oauth2"
)

const cookieName = "__Host-iulita_oauth_browser"

// Limits bounds request admission and browser-flow processing.
type Limits struct{ AuthorizePerIP, LoginPerIP, LoginPerAccount, ConsentPerIP int }

// Config collects dependencies and limits for the mobile server.
type Config struct {
	Clock    *serverclock.Clock
	Store    *oauthstore.Store
	Accounts Accounts
	Scopes   []string
	AppName  string
	Limits   Limits
}

// Server serves the independent mobile protocol.
type Server struct {
	config       Config
	provider     fosite.OAuth2Provider
	strategy     oauth2.CoreStrategy
	origin, host string
}

// New constructs the subsystem with explicit dependencies and limits.
func New(c Config) (*Server, error) {
	if c.Store == nil || c.Accounts == nil || len(c.Scopes) == 0 {
		return nil, errors.New("mobile browser OAuth dependencies required")
	}
	u, err := url.Parse(c.Store.Issuer())
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" {
		return nil, errors.New("mobile browser OAuth requires an exact HTTPS origin issuer")
	}
	if c.AppName == "" {
		c.AppName = "Iulita"
	}
	if c.Limits.AuthorizePerIP <= 0 {
		c.Limits.AuthorizePerIP = 30
	}
	if c.Limits.LoginPerIP <= 0 {
		c.Limits.LoginPerIP = 10
	}
	if c.Limits.LoginPerAccount <= 0 {
		c.Limits.LoginPerAccount = 5
	}
	if c.Limits.ConsentPerIP <= 0 {
		c.Limits.ConsentPerIP = 30
	}
	if c.Clock == nil {
		c.Clock = serverclock.New()
	}
	cfg := c.Store.FrameworkConfig()
	strategy := c.Store.Strategy()
	p := compose.Compose(cfg, c.Store, strategy, compose.OAuth2AuthorizeExplicitFactory, compose.OAuth2RefreshTokenGrantFactory, compose.OAuth2PKCEFactory)
	return &Server{config: c, provider: p, strategy: strategy, origin: c.Store.Issuer(), host: u.Host}, nil
}

// Handler returns the browser OAuth HTTP handler.
func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.serve) }
func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	// Never infer TLS, issuer, client IP or return URL from untrusted forwarding
	// headers. BE19 must supply real TLS or a deliberately trusted ingress adapter.
	if r.TLS == nil || r.Host != s.host {
		s.fail(w, 400, "Неверный адрес сервера или небезопасное соединение.")
		return
	}
	switch r.URL.Path {
	case "/.well-known/oauth-authorization-server":
		if r.Method != "GET" {
			s.fail(w, 405, "Метод недоступен.")
			return
		}
		s.metadata(w)
	case "/oauth2/time":
		s.timeSample(w, r)
	case "/oauth2/jwks":
		if r.Method != "GET" {
			s.fail(w, 405, "Метод недоступен.")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if writeErr := json.NewEncoder(w).Encode(s.config.Store.PublicJWKS()); writeErr != nil {
			return
		}
	case "/oauth2/authorize":
		if r.Method != "GET" {
			s.fail(w, 405, "Метод недоступен.")
			return
		}
		s.authorize(w, r)
	case "/oauth2/login":
		if r.Method != "POST" {
			s.fail(w, 405, "Метод недоступен.")
			return
		}
		s.login(w, r)
	case "/oauth2/consent":
		if r.Method != "POST" {
			s.fail(w, 405, "Метод недоступен.")
			return
		}
		s.consent(w, r)
	case "/oauth2/device_authorization":
		s.deviceAuthorization(w, r)
	case "/oauth2/device":
		s.deviceVerification(w, r)
	case "/oauth2/token":
		s.token(w, r)
	case "/oauth2/revoke":
		s.revoke(w, r)
	default:
		s.fail(w, 404, "Страница не найдена.")
	}
}
func (s *Server) metadata(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	if writeErr := json.NewEncoder(w).Encode(map[string]any{"issuer": s.origin, "iulita_time_endpoint": s.origin + "/oauth2/time", "authorization_endpoint": s.origin + "/oauth2/authorize", "token_endpoint": s.origin + "/oauth2/token", "revocation_endpoint": s.origin + "/oauth2/revoke", "jwks_uri": s.origin + "/oauth2/jwks", "response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code", "refresh_token", oauthstore.DeviceGrant, "client_credentials"}, "device_authorization_endpoint": s.origin + "/oauth2/device_authorization", "token_endpoint_auth_methods_supported": []string{"none", "client_secret_basic"}, "code_challenge_methods_supported": []string{"S256"}, "scopes_supported": s.config.Scopes, "iulita_mobile_protocol_version": 1, "authorization_response_iss_parameter_supported": true}); writeErr != nil {
		return
	}
}
func one(values url.Values, allowed []string) bool {
	set := map[string]bool{}
	for _, k := range allowed {
		set[k] = true
	}
	for k, v := range values {
		if !set[k] || len(v) != 1 {
			return false
		}
	}
	return true
}
func has(list []string, value string) bool {
	for _, s := range list {
		if s == value {
			return true
		}
	}
	return false
}
func validID(id string) bool {
	u, err := uuid.Parse(id)
	return err == nil && u != uuid.Nil && u.String() == id
}
func (s *Server) parseAuthorization(r *http.Request) (oauthstore.Authorization, error) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || !one(q, []string{"response_type", "client_id", "redirect_uri", "scope", "state", "code_challenge", "code_challenge_method", "iulita_installation_key_thumbprint", "iulita_device_id", "iulita_fresh_auth"}) {
		return oauthstore.Authorization{}, oauthstore.ErrInvalid
	}
	a := oauthstore.Authorization{ClientID: q.Get("client_id"), RedirectURI: q.Get("redirect_uri"), Scope: q.Get("scope"), State: q.Get("state"), Challenge: q.Get("code_challenge"), KeyThumbprint: q.Get("iulita_installation_key_thumbprint"), DeviceHint: q.Get("iulita_device_id"), FreshAuth: q.Get("iulita_fresh_auth") == "true"}
	if q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || !oauthstore.ValidSecret(a.Challenge) || !oauthstore.ValidSecret(a.KeyThumbprint) || len(a.State) < 16 || len(a.State) > 512 || strings.ContainsAny(a.State, "\r\n\x00") || (a.DeviceHint != "" && !validID(a.DeviceHint)) || (q.Has("iulita_fresh_auth") && q.Get("iulita_fresh_auth") != "true" && q.Get("iulita_fresh_auth") != "false") {
		return a, oauthstore.ErrInvalid
	}
	client, err := s.config.Store.GetClient(r.Context(), a.ClientID)
	if err != nil || !client.IsPublic() || !has(client.GetRedirectURIs(), a.RedirectURI) {
		return a, oauthstore.ErrInvalid
	}
	scopes := strings.Fields(a.Scope)
	if len(scopes) == 0 || len(scopes) > 32 {
		return a, oauthstore.ErrInvalid
	}
	seen := map[string]bool{}
	for _, scope := range scopes {
		if seen[scope] || !has(s.config.Scopes, scope) || !has(client.GetScopes(), scope) {
			return a, oauthstore.ErrInvalid
		}
		seen[scope] = true
	}
	a.Scope = strings.Join(scopes, " ")
	return a, nil
}
func browserCookie(r *http.Request) string {
	var value string
	count := 0
	for _, c := range r.Cookies() {
		if c.Name == cookieName {
			count++
			value = c.Value
		}
	}
	if count != 1 || !oauthstore.ValidSecret(value) {
		return ""
	}
	return value
}
func setCookie(w http.ResponseWriter, secret string) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: secret, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 600})
}
func (s *Server) limit(w http.ResponseWriter, r *http.Request, category, subject string, n int) bool {
	ok, err := s.config.Store.AllowBrowserAttempt(r.Context(), category, subject, n, 10*time.Minute)
	if err != nil {
		s.fail(w, 503, "Сервис временно недоступен.")
		return false
	}
	if !ok {
		w.Header().Set("Retry-After", "600")
		s.fail(w, 429, "Слишком много попыток. Повторите позже.")
		return false
	}
	return true
}
func ip(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}
func authURL(a oauthstore.Authorization) string {
	q := url.Values{"response_type": {"code"}, "client_id": {a.ClientID}, "redirect_uri": {a.RedirectURI}, "scope": {a.Scope}, "state": {a.State}, "code_challenge": {a.Challenge}, "code_challenge_method": {"S256"}, "iulita_installation_key_thumbprint": {a.KeyThumbprint}}
	if a.DeviceHint != "" {
		q.Set("iulita_device_id", a.DeviceHint)
	}
	if a.FreshAuth {
		q.Set("iulita_fresh_auth", "true")
	}
	return "/oauth2/authorize?" + q.Encode()
}
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	if len(r.URL.RawQuery) > 8192 {
		s.fail(w, 400, "Неверный запрос входа.")
		return
	}
	if !s.limit(w, r, "authorize.ip", ip(r), s.config.Limits.AuthorizePerIP) {
		return
	}
	a, err := s.parseAuthorization(r)
	if err != nil {
		s.fail(w, 400, "Неверный запрос входа.")
		return
	}
	// Validate the registered client/redirect/scopes with Fosite before any page
	// or flow is stored. Never redirect an invalid authorization request.
	check := r.Clone(r.Context())
	check.URL, err = url.Parse(s.origin + authURL(a))
	if err != nil {
		s.fail(w, 400, "Неверный запрос входа.")
		return
	}
	check.Host = s.host
	if _, err = s.provider.NewAuthorizeRequest(r.Context(), check); err != nil {
		s.fail(w, 400, "Неверный запрос входа.")
		return
	}
	cookie := browserCookie(r)
	if cookie == "" {
		cookie, err = oauthstore.Secret()
		if err != nil {
			s.fail(w, 503, "Сервис временно недоступен.")
			return
		}
	}
	flow, csrf, err := s.config.Store.CreateBrowserFlow(r.Context(), cookie, a)
	if err != nil {
		s.fail(w, 503, "Сервис временно недоступен.")
		return
	}
	setCookie(w, cookie)
	s.page(w, pageData{Flow: flow.ID, CSRF: csrf, App: s.config.AppName, Login: true})
}
func (s *Server) form(w http.ResponseWriter, r *http.Request, allowed []string) (url.Values, bool) {
	if r.Header.Get("Origin") != s.origin || r.URL.RawQuery != "" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		s.fail(w, 400, "Неверный запрос формы.")
		return nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if r.ParseForm() != nil || !one(r.PostForm, allowed) {
		s.fail(w, 400, "Неверный запрос формы.")
		return nil, false
	}
	return r.PostForm, true
}
func available(a Account) bool {
	return validID(a.ID) && !a.Disabled && !a.MustChangePassword && a.CredentialVersion != ""
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	form, ok := s.form(w, r, []string{"flow_id", "csrf", "username", "password"})
	if !ok {
		return
	}
	cookie := browserCookie(r)
	if _, err := s.config.Store.CheckBrowserFlow(r.Context(), cookie, form.Get("flow_id"), form.Get("csrf"), "login"); err != nil {
		s.fail(w, 400, "Вход устарел. Вернитесь в приложение и начните снова.")
		return
	}
	if !s.limit(w, r, "login.ip", ip(r), s.config.Limits.LoginPerIP) || !s.limit(w, r, "login.account", strings.ToLower(strings.TrimSpace(form.Get("username"))), s.config.Limits.LoginPerAccount) {
		return
	}
	if len(form.Get("username")) > 128 || len(form.Get("password")) > 72 {
		s.fail(w, 400, "Неверные данные входа.")
		return
	}
	a, err := s.config.Accounts.Authenticate(r.Context(), form.Get("username"), form.Get("password"))
	if err != nil || !available(a) {
		s.page(w, pageData{Flow: form.Get("flow_id"), CSRF: form.Get("csrf"), App: s.config.AppName, Login: true, Error: "Не удалось войти. Проверьте данные и доступность аккаунта."})
		return
	}
	// The timestamp is generated here after a real credential check, never from
	// a cookie, client-supplied auth_time, prior JWT or hidden form field.
	flow, rotated, csrf, err := s.config.Store.LoginBrowserFlow(r.Context(), cookie, form.Get("flow_id"), form.Get("csrf"), a.ID, s.config.Store.CredentialStamp(a.ID+"\x00"+a.CredentialVersion), time.Now().UTC())
	if err != nil {
		s.fail(w, 400, "Вход устарел. Начните снова из приложения.")
		return
	}
	setCookie(w, rotated)
	appName := s.config.AppName
	if registration, policyErr := s.config.Store.ClientPolicy(r.Context(), flow.Request.ClientID); policyErr == nil {
		appName = registration.Name
	}
	s.page(w, pageData{Flow: flow.ID, CSRF: csrf, App: appName, Device: flow.Request.DeviceFlowID != "", DisplayName: a.DisplayName, Scopes: scopeLabels(flow.Request.Scope), ReturnHost: s.host})
}
func (s *Server) consent(w http.ResponseWriter, r *http.Request) {
	form, ok := s.form(w, r, []string{"flow_id", "csrf", "decision", "user_code"})
	if !ok {
		return
	}
	if !s.limit(w, r, "consent.ip", ip(r), s.config.Limits.ConsentPerIP) {
		return
	}
	cookie := browserCookie(r)
	flow, err := s.config.Store.CheckBrowserFlow(r.Context(), cookie, form.Get("flow_id"), form.Get("csrf"), "consent")
	if err != nil {
		s.fail(w, 400, "Подтверждение устарело. Начните снова из приложения.")
		return
	}
	a, err := s.config.Accounts.Lookup(r.Context(), flow.OwnerID)
	if err != nil || !available(a) || a.ID != flow.OwnerID {
		s.fail(w, 403, "Аккаунт недоступен для входа.")
		return
	}

	decision := form.Get("decision")
	if decision != "approve" && decision != "deny" {
		s.fail(w, 400, "Выберите действие.")
		return
	}
	location := ""
	err = s.config.Accounts.Guard(r.Context(), flow.OwnerID, func(a Account) error {
		if !available(a) || a.ID != flow.OwnerID {
			return oauthstore.ErrBrowserFlow
		}
		if stepErr1 := s.config.Store.SyncAccount(r.Context(), a.ID, a.CredentialVersion, true); stepErr1 != nil {
			return stepErr1
		}
		return s.config.Store.AuthorizeBrowserFlow(r.Context(), cookie, flow.ID, form.Get("csrf"), s.config.Store.CredentialStamp(a.ID+"\x00"+a.CredentialVersion), func(ctx context.Context, f oauthstore.BrowserFlow) error {
			if f.Request.DeviceFlowID != "" {
				return s.config.Store.DecideDeviceAuthorization(ctx, f, form.Get("user_code"), decision == "approve")
			}
			if form.Has("user_code") {
				return oauthstore.ErrInvalid
			}
			if decision == "deny" {
				u, parseErr := url.Parse(f.Request.RedirectURI)
				if parseErr != nil {
					return parseErr
				}
				q := u.Query()
				q.Set("error", "access_denied")
				q.Set("state", f.Request.State)
				q.Set("iss", s.origin)
				u.RawQuery = q.Encode()
				location = u.String()
				return nil
			}
			session, stepErr2 := s.config.Store.PrepareAuthorization(ctx, f)
			if stepErr2 != nil {
				return stepErr2
			}
			request := r.Clone(ctx)
			request.Method = "GET"
			request.Body = http.NoBody
			request.Form = nil
			request.PostForm = nil
			parsedURL, parseErr := url.Parse(s.origin + authURL(f.Request))
			if parseErr != nil {
				return parseErr
			}
			request.URL = parsedURL
			request.Host = s.host
			ar, stepErr2 := s.provider.NewAuthorizeRequest(ctx, request)
			if stepErr2 != nil {
				return stepErr2
			}
			ar.SetID(session.Binding.FamilyID)
			// Consent scopes stay in the family. Pending/provisional credentials have
			// no scopes until installation-key proof and stage completion (BE16/BE17).
			response, stepErr2 := s.provider.NewAuthorizeResponse(ctx, ar, session)
			if stepErr2 != nil {
				return stepErr2
			}
			target := *ar.GetRedirectURI()
			q := target.Query()
			for k, v := range response.GetParameters() {
				q[k] = v
			}
			q.Set("iss", s.origin)
			target.RawQuery = q.Encode()
			location = target.String()
			return nil
		})
	})
	if err != nil {
		s.fail(w, 400, "Не удалось завершить вход. Начните снова из приложения.")
		return
	}
	if flow.Request.DeviceFlowID != "" {
		s.fail(w, 200, "Решение сохранено. Вернитесь в консоль. Не передавайте код другим людям.")
		return
	}
	// A 303 after form POST never forwards credentials to the app callback.
	w.Header().Set("Location", location)
	w.WriteHeader(http.StatusSeeOther)
}

type pageData struct {
	Flow, CSRF, App, DisplayName, ReturnHost, Error string
	Device                                          bool
	Login                                           bool
	Scopes                                          []string
}

var page = template.Must(template.New("oauth").Parse(`<!doctype html><html lang="ru"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Вход в Iulita</title><main><h1>Вход в {{.App}}</h1>{{if .Error}}<p role="alert">{{.Error}}</p>{{end}}{{if .Login}}<form method="post" action="/oauth2/login"><input type="hidden" name="flow_id" value="{{.Flow}}"><input type="hidden" name="csrf" value="{{.CSRF}}"><p><label>Имя пользователя <input name="username" autocomplete="username" required maxlength="128"></label></p><p><label>Пароль <input type="password" name="password" autocomplete="current-password" required maxlength="72"></label></p><button type="submit">Войти</button></form>{{else if .Flow}}<p>Аккаунт: {{.DisplayName}}</p><p>Приложение запрашивает:</p><ul>{{range .Scopes}}<li>{{.}}</li>{{end}}</ul><p>Разрешения Android и управление телефоном включаются отдельно на самом устройстве.</p><form method="post" action="/oauth2/consent"><input type="hidden" name="flow_id" value="{{.Flow}}"><input type="hidden" name="csrf" value="{{.CSRF}}">{{if .Device}}<p>Подтверждайте вход только если вы сами начали его в своей консоли. Сверьте имя приложения и введите код, показанный консолью.</p><label>Код из консоли <input name="user_code" required maxlength="9" autocomplete="off"></label>{{end}}<button type="submit" name="decision" value="approve">Разрешить вход</button><button type="submit" name="decision" value="deny">Отказаться</button></form>{{end}}</main></html>`))

func (s *Server) page(w http.ResponseWriter, data pageData) {
	// no-referrer makes navigational form POSTs carry Origin: null. Preserve
	// same-origin browser submissions while denying referrers to other origins.
	// Non-form responses and the authorization-code redirect stay no-referrer.
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if writeErr := page.Execute(w, data); writeErr != nil {
		return
	}
}
func (s *Server) fail(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if writeErr := page.Execute(w, pageData{App: s.config.AppName, Error: message}); writeErr != nil {
		return
	}
}
func scopeLabels(scope string) []string {
	labels := map[string]string{"chats:read": "Читать ваши чаты", "chats:write": "Отправлять сообщения в ваши чаты", "devices:read": "Просматривать ваши устройства", "devices:manage": "Управлять привязкой ваших устройств", "devices:receive": "Получать задачи для телефона", "devices:sync": "Синхронизировать состояние задач", "phone:data": "Передавать выбранные данные телефона", "phone:ui": "Управлять разрешёнными приложениями"}
	out := make([]string, 0, len(strings.Fields(scope)))
	for _, s := range strings.Fields(scope) {
		v := labels[s]
		if v == "" {
			v = s
		}
		out = append(out, v)
	}
	return out
}

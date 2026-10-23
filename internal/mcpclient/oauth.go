package mcpclient

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// OAuthBinding contains encrypted owner-specific OAuth material; SDK OAuth and
// credential fallback are deliberately not used.
type OAuthBinding struct {
	Issuer        string    `json:"issuer"`
	ClientID      string    `json:"client_id"`
	Resource      string    `json:"resource"`
	TokenEndpoint string    `json:"token_endpoint"`
	RefreshToken  string    `json:"refresh_token,omitempty"`
	Scopes        string    `json:"scopes"`
	Expiry        time.Time `json:"expiry"`
}

// OAuthStart specifies a preregistered public client and deployment-approved issuer.
type OAuthStart struct {
	ClientID string `json:"client_id"`
	Issuer   string `json:"issuer"`
	Scopes   string `json:"scopes"`
	Revision int64  `json:"revision"`
}

// OAuthFlow binds a one-shot browser authorization to its owner, cookie and policy epoch.
type OAuthFlow struct {
	State          string       `json:"state"`
	Owner          string       `json:"owner"`
	ConnectionID   string       `json:"connection_id"`
	Generation     int64        `json:"generation"`
	PolicyRevision int64        `json:"policy_revision"`
	Verifier       string       `json:"verifier"`
	CookieHash     string       `json:"cookie_hash"`
	Redirect       string       `json:"redirect"`
	Binding        OAuthBinding `json:"binding"`
	ExpiresAt      time.Time    `json:"expires_at"`
	Used           bool         `json:"used"`
}

// RefreshAuth serializes token rotation per connection. A refresh never enables
// a disconnected connection, widens scopes or replays a tool invocation.
func (s *Service) RefreshAuth(ctx context.Context, sc Scope, id string) error {
	if s.admit(ctx, sc) != nil {
		return ErrDenied
	}
	for {
		s.mu.Lock()
		c, e := s.getLocked(sc, id)
		if e != nil {
			s.mu.Unlock()
			return e
		}
		if c.AuthState == "refresh_failed" {
			s.mu.Unlock()
			return ErrUnavailable
		}
		if c.OAuth == nil || c.OAuth.Expiry.IsZero() || time.Until(c.OAuth.Expiry) > 30*time.Second {
			s.mu.Unlock()
			return nil
		}
		if pending := s.refreshing[id]; pending != nil {
			s.mu.Unlock()
			select {
			case <-pending:
				continue
			case <-ctx.Done():
				return ErrUnavailable
			}
		}
		if c.OAuth.RefreshToken == "" || !s.policy.Enabled {
			c.AuthState = "expired"
			saveErr := s.saveLocked(sc.OwnerID, id, "connection", c)
			if saveErr != nil {
				s.mu.Unlock()
				return saveErr
			}
			s.mu.Unlock()
			return ErrUnavailable
		}
		wait := make(chan struct{})
		s.refreshing[id] = wait
		binding := *c.OAuth
		gen := c.Generation
		policyRevision := s.policy.Revision
		s.mu.Unlock()
		var token struct {
			Access  string `json:"access_token"`
			Refresh string `json:"refresh_token"`
			Type    string `json:"token_type"`
			Expires int64  `json:"expires_in"`
			Scope   string `json:"scope"`
		}
		err := s.authHTTP(ctx, "POST", binding.TokenEndpoint, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {binding.RefreshToken}, "client_id": {binding.ClientID}, "resource": {binding.Resource}}, &token)
		if token.Access == "" || !strings.EqualFold(token.Type, "Bearer") || len(token.Access) > 16384 || strings.ContainsAny(token.Access, "\r\n") || (token.Expires < 1 || token.Expires > 31536000) {
			err = ErrUnavailable
		}
		if token.Scope != "" && !sameScopes(token.Scope, binding.Scopes) {
			err = ErrDenied
		}
		s.mu.Lock()
		delete(s.refreshing, id)
		close(wait)
		c, e = s.getLocked(sc, id)
		if e != nil || c.Generation != gen || !s.policy.Enabled || s.policy.Revision != policyRevision {
			s.mu.Unlock()
			return ErrConflict
		}
		if err != nil {
			c.AuthState = "refresh_failed"
			saveErr := s.saveLocked(sc.OwnerID, id, "connection", c)
			if saveErr != nil {
				s.mu.Unlock()
				return saveErr
			}
			s.mu.Unlock()
			return ErrUnavailable
		}
		s.stopLocked(id)
		c.Token = token.Access
		c.Secret = token.Access
		c.OAuth.Expiry = time.Now().Add(time.Duration(token.Expires) * time.Second)
		if token.Refresh != "" {
			c.OAuth.RefreshToken = token.Refresh
		}
		c.AuthState = "valid"
		c.Revision++
		c.Generation++
		err = s.saveLocked(sc.OwnerID, id, "connection", c)
		s.mu.Unlock()
		return err
	}
}
func sameScopes(a, b string) bool {
	x := map[string]bool{}
	for _, s := range strings.Fields(a) {
		x[s] = true
	}
	y := map[string]bool{}
	for _, s := range strings.Fields(b) {
		y[s] = true
	}
	if len(x) != len(y) {
		return false
	}
	for k := range x {
		if !y[k] {
			return false
		}
	}
	return true
}

func nonce() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("secure randomness unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func digest(v string) string {
	sum := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (s *Service) authHTTP(ctx context.Context, method, endpoint string, form url.Values, out any) error {
	if _, e := validateEndpoint(endpoint); e != nil {
		return ErrDenied
	}
	s.mu.Lock()
	allowed := s.originAllowedLocked(endpoint) && s.policy.Enabled
	s.mu.Unlock()
	if !allowed {
		return ErrDenied
	}
	p, ok := s.protocol.(*SDKProtocol)
	if !ok || p.Transport == nil {
		return ErrUnavailable
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	r, e := http.NewRequestWithContext(ctx, method, endpoint, body)
	if e != nil {
		return ErrInvalid
	}
	r.Header.Set("Accept", "application/json")
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	client := &http.Client{Transport: p.Transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrDenied }}
	response, e := client.Do(r)
	if e != nil {
		return ErrUnavailable
	}
	defer closeQuietly(response.Body)
	b, e := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if e != nil || response.StatusCode != 200 || validJSON(b, 1<<20) != nil {
		return ErrUnavailable
	}
	if json.Unmarshal(b, out) != nil {
		return ErrUnavailable
	}
	return nil
}

// BeginOAuth uses a preregistered public client and an explicitly approved issuer.
// Dynamic client registration is intentionally unavailable, not guessed.
func (s *Service) BeginOAuth(ctx context.Context, sc Scope, id string, in OAuthStart, redirect, cookie string) (string, error) {
	if s.admit(ctx, sc) != nil || sc.CallerID != "dashboard" || cookie == "" || in.ClientID == "" || len(in.ClientID) > 2048 || len(in.Scopes) > 2048 {
		return "", ErrDenied
	}
	u, e := validateEndpoint(redirect)
	if e != nil || u.RawQuery != "" {
		return "", ErrInvalid
	}
	issuer, e := validateEndpoint(in.Issuer)
	if e != nil || issuer.RawQuery != "" {
		return "", ErrInvalid
	}
	s.mu.Lock()
	c, e := s.getLocked(sc, id)
	if e != nil {
		s.mu.Unlock()
		return "", e
	}
	if c.Revision != in.Revision {
		s.mu.Unlock()
		return "", ErrConflict
	}
	if c.Transport != "http" {
		s.mu.Unlock()
		return "", ErrInvalid
	}
	gen := c.Generation
	policyRevision := s.policy.Revision
	resource := c.Endpoint
	s.mu.Unlock()
	var protected struct {
		Resource string   `json:"resource"`
		Servers  []string `json:"authorization_servers"`
	}
	resourceURL, parseErr := url.Parse(resource)
	if parseErr != nil {
		return "", ErrInvalid
	}
	protectedURL := resourceURL.Scheme + "://" + resourceURL.Host + "/.well-known/oauth-protected-resource" + resourceURL.Path
	if err := s.authHTTP(ctx, "GET", protectedURL, nil, &protected); err != nil {
		if err := s.authHTTP(ctx, "GET", resourceURL.Scheme+"://"+resourceURL.Host+"/.well-known/oauth-protected-resource", nil, &protected); err != nil {
			return "", err
		}
	}
	if protected.Resource != resource {
		return "", ErrDenied
	}
	matches := false
	for _, as := range protected.Servers {
		if as == in.Issuer {
			matches = true
		}
	}
	if !matches {
		return "", ErrDenied
	}
	var metadata struct {
		Issuer                string   `json:"issuer"`
		AuthorizationEndpoint string   `json:"authorization_endpoint"`
		TokenEndpoint         string   `json:"token_endpoint"`
		Challenges            []string `json:"code_challenge_methods_supported"`
	}
	wellKnown := issuer.Scheme + "://" + issuer.Host + "/.well-known/oauth-authorization-server" + strings.TrimSuffix(issuer.Path, "/")
	if requestErr := s.authHTTP(ctx, "GET", wellKnown, nil, &metadata); requestErr != nil {
		return "", requestErr
	}
	if metadata.Issuer != in.Issuer {
		return "", ErrDenied
	}
	pkce := false
	for _, m := range metadata.Challenges {
		if m == "S256" {
			pkce = true
		}
	}
	if !pkce {
		return "", ErrDenied
	}
	for _, endpoint := range []string{metadata.AuthorizationEndpoint, metadata.TokenEndpoint} {
		if _, e = validateEndpoint(endpoint); e != nil {
			return "", ErrDenied
		}
		s.mu.Lock()
		ok := s.originAllowedLocked(endpoint)
		s.mu.Unlock()
		if !ok {
			return "", ErrDenied
		}
	}
	flow := OAuthFlow{State: nonce(), Owner: sc.OwnerID, ConnectionID: id, Generation: gen, PolicyRevision: policyRevision, Verifier: nonce(), CookieHash: digest(cookie), Redirect: redirect, Binding: OAuthBinding{Issuer: in.Issuer, ClientID: in.ClientID, Resource: resource, TokenEndpoint: metadata.TokenEndpoint, Scopes: in.Scopes}, ExpiresAt: time.Now().Add(10 * time.Minute)}
	s.mu.Lock()
	current, e := s.getLocked(sc, id)
	if e != nil || current.Generation != gen || s.policy.Revision != policyRevision {
		s.mu.Unlock()
		return "", ErrConflict
	}
	e = s.saveLocked(sc.OwnerID, "oauth-"+digest(flow.State), "oauth", flow)
	s.mu.Unlock()
	if e != nil {
		return "", e
	}
	authURL, parseErr := url.Parse(metadata.AuthorizationEndpoint)
	if parseErr != nil {
		return "", ErrInvalid
	}
	q := authURL.Query()
	q.Set("response_type", "code")
	q.Set("client_id", in.ClientID)
	q.Set("redirect_uri", redirect)
	q.Set("scope", in.Scopes)
	q.Set("state", flow.State)
	q.Set("resource", resource)
	q.Set("code_challenge", digest(flow.Verifier))
	q.Set("code_challenge_method", "S256")
	authURL.RawQuery = q.Encode()
	return authURL.String(), nil
}

// FinishOAuth consumes browser state once before exchanging an owner-bound code.
func (s *Service) FinishOAuth(ctx context.Context, state, code, issuer, cookie string) error {
	if len(state) > 128 || len(code) > 8192 || state == "" || code == "" || cookie == "" {
		return ErrDenied
	}
	row, e := s.repo.GetMCPOAuthFlow(ctx, "oauth-"+digest(state))
	if e != nil {
		return ErrDenied
	}
	p, err := s.vault.OpenPrivate(row.Payload)
	var flow OAuthFlow
	if err != nil || json.Unmarshal([]byte(p), &flow) != nil || row.OwnerID != flow.Owner {
		return ErrDenied
	}
	flowRevision := row.Revision
	found := true

	if !found || flow.Used || time.Now().After(flow.ExpiresAt) || flow.CookieHash != digest(cookie) || flow.Binding.Issuer != issuer {
		return ErrDenied
	}
	sc := Scope{OwnerID: flow.Owner, CallerID: "dashboard", Private: true}
	if s.admit(ctx, sc) != nil {
		return ErrDenied
	}
	s.mu.Lock()
	c, e := s.getLocked(sc, flow.ConnectionID)
	if e != nil || c.Generation != flow.Generation || !s.policy.Enabled || s.policy.Revision != flow.PolicyRevision || s.revisions["oauth-"+digest(state)] != flowRevision {
		s.mu.Unlock()
		return ErrConflict
	}
	flow.Used = true
	if requestErr := s.saveLocked(flow.Owner, "oauth-"+digest(state), "oauth", flow); requestErr != nil {
		s.mu.Unlock()
		return requestErr
	}
	s.mu.Unlock()
	var token struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		Type    string `json:"token_type"`
		Expires int64  `json:"expires_in"`
		Scope   string `json:"scope"`
	}
	if requestErr := s.authHTTP(ctx, "POST", flow.Binding.TokenEndpoint, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {flow.Binding.ClientID}, "redirect_uri": {flow.Redirect}, "code_verifier": {flow.Verifier}, "resource": {flow.Binding.Resource}}, &token); requestErr != nil {
		return requestErr
	}
	if token.Access == "" || !strings.EqualFold(token.Type, "Bearer") || strings.ContainsAny(token.Access, "\r\n") || len(token.Access) > 16384 || token.Scope != "" && !sameScopes(token.Scope, flow.Binding.Scopes) {
		return ErrDenied
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, e = s.getLocked(sc, flow.ConnectionID)
	if e != nil || c.Generation != flow.Generation || !s.policy.Enabled || s.policy.Revision != flow.PolicyRevision {
		return ErrConflict
	}
	s.stopLocked(c.ID)
	c.Token = token.Access
	c.Secret = token.Access
	c.AuthMode = "oauth"
	c.AuthState = "valid"
	c.SecretHeaders = nil
	c.Headers = nil
	flow.Binding.RefreshToken = token.Refresh
	if token.Expires > 0 && token.Expires <= 31536000 {
		flow.Binding.Expiry = time.Now().Add(time.Duration(token.Expires) * time.Second)
	}
	if token.Scope != "" {
		flow.Binding.Scopes = token.Scope
	}
	c.OAuth = &flow.Binding
	c.Generation++
	c.Revision++
	c.Desired = "disabled"
	c.Health = "stopped"
	for i := range c.Items {
		c.Items[i].Allowed = false
	}
	return s.saveLocked(sc.OwnerID, c.ID, "connection", c)
}

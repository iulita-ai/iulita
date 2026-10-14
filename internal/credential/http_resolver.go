package credential

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/httpfetch"
)

// HTTPResolver is the narrow credential resolver for the HTTP engine. It is
// deliberately NOT CredentialProvider: no env/config/keyring fallback and no
// global-scope resolution without an explicit published grant (plan §8.1).
// Every resolution checks owner, origin, caller, usages, and expiry.
type HTTPResolver struct {
	store *Store
}

// NewHTTPResolver wraps a credential store.
func NewHTTPResolver(s *Store) *HTTPResolver { return &HTTPResolver{store: s} }

// usagesAll is the effective usage set when a grant declares none.
var usagesAll = map[string]bool{
	"basic": true, "bearer": true, "header": true, "multipart": true,
}

// ResolveHTTP resolves a credential reference for one exact origin and usage.
func (r *HTTPResolver) ResolveHTTP(ctx context.Context, scope httpfetch.Scope, ref string, origin httpfetch.Origin, usage httpfetch.CredentialUsage) (*httpfetch.ResolvedCredential, error) {
	if scope.UserID == "" {
		// No authenticated owner: fail closed for credentials.
		return nil, fmt.Errorf("%w: no authenticated user in scope", ErrCredentialDenied)
	}
	if r.store == nil || r.store.repo == nil {
		return nil, fmt.Errorf("%w: credential store unavailable", ErrCredentialDenied)
	}

	// Owner-scoped first; global only via published grants.
	cred, err := r.store.repo.GetCredentialByNameAndOwner(ctx, ref, scope.UserID)
	if err != nil {
		return nil, fmt.Errorf("credential %q: %w", ref, err)
	}
	principalType := domain.HTTPGrantPrincipalUser
	principalID := scope.UserID
	if cred == nil {
		cred, err = r.store.repo.GetCredentialByName(ctx, ref)
		if err != nil || cred == nil {
			return nil, fmt.Errorf("%w: no credential %q for this owner", ErrCredentialDenied, ref)
		}
		principalType = domain.HTTPGrantPrincipalGlobal
		principalID = ""
	}

	// Expiry/revocation on the credential itself.
	if cred.ExpiresAt != nil && cred.ExpiresAt.Before(time.Now()) {
		return nil, fmt.Errorf("%w: credential expired", ErrCredentialDenied)
	}

	grants, err := r.store.repo.ListHTTPCredentialGrants(ctx, cred.ID)
	if err != nil {
		return nil, fmt.Errorf("credential %q grants: %w", ref, err)
	}
	if !grantMatches(grants, principalType, principalID, scope.Caller.ID, origin, string(usage), time.Now()) {
		return nil, fmt.Errorf("%w: no grant for %s on %s", ErrCredentialDenied, usage, origin)
	}

	val, ok := r.store.decrypt(ref, cred)
	if !ok {
		return nil, fmt.Errorf("%w: credential value unavailable", ErrCredentialDenied)
	}

	switch cred.Type {
	case domain.CredentialTypeHTTPBasic:
		var pair struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.Unmarshal([]byte(val), &pair); err != nil {
			return nil, fmt.Errorf("%w: http_basic credential is malformed", ErrCredentialDenied)
		}
		if strings.Contains(pair.Username, ":") {
			return nil, fmt.Errorf("%w: basic username must not contain ':'", ErrCredentialDenied)
		}
		return &httpfetch.ResolvedCredential{Kind: "basic", Username: pair.Username, Password: pair.Password}, nil
	case domain.CredentialTypeBearer:
		return &httpfetch.ResolvedCredential{Kind: "bearer", Token: val}, nil
	default:
		// api_key and other types serve as opaque secret header values.
		return &httpfetch.ResolvedCredential{Kind: "secret", Secret: val}, nil
	}
}

// grantMatches enforces principal/caller/origin/usage/expiry on grants.
func grantMatches(grants []domain.HTTPCredentialGrant, principalType, principalID, callerID string, origin httpfetch.Origin, usage string, now time.Time) bool {
	for i := range grants {
		g := &grants[i]
		if g.PrincipalType != principalType {
			continue
		}
		if principalType == domain.HTTPGrantPrincipalUser && g.PrincipalID != principalID {
			continue
		}
		if g.CallerID != "" && callerID != "" && g.CallerID != callerID {
			continue
		}
		if !strings.EqualFold(g.Scheme, origin.Scheme) ||
			!strings.EqualFold(g.Host, origin.Host) ||
			g.Port != origin.Port {
			continue
		}
		if g.ExpiresAt != nil && g.ExpiresAt.Before(now) {
			continue
		}
		var usages []string
		if g.Usages != "" && g.Usages != "[]" {
			if err := json.Unmarshal([]byte(g.Usages), &usages); err != nil {
				continue
			}
		}
		if len(usages) == 0 {
			if usagesAll[usage] {
				return true
			}
			continue
		}
		for _, u := range usages {
			if u == usage {
				return true
			}
		}
	}
	return false
}

package engine

import (
	"context"
	"time"

	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
)

// Authority captures verified caller authority without retaining bearer credentials.
type Authority = oauthstore.Authority
type authorityKey struct{}

// WithAuthority binds a verified caller snapshot to an execution context.
func WithAuthority(ctx context.Context, a Authority) context.Context {
	a.Scopes = append([]string(nil), a.Scopes...)
	return context.WithValue(ctx, authorityKey{}, a)
}

// AuthorityFrom returns the caller authority bound to an execution context.
func AuthorityFrom(ctx context.Context) Authority {
	a, ok := ctx.Value(authorityKey{}).(Authority)
	if !ok {
		return Authority{}
	}
	a.Scopes = append([]string(nil), a.Scopes...)
	return a
}
func permits(a Authority, t Tool) bool {
	for _, scope := range t.RequiredScopes {
		found := false
		for _, allowed := range a.Scopes {
			if allowed == scope {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	if t.FreshAuth && (a.AuthTime <= 0 || time.Unix(a.AuthTime, 0).Before(time.Now().Add(-5*time.Minute)) || time.Unix(a.AuthTime, 0).After(time.Now().Add(time.Second))) {
		return false
	}
	return true
}

type externalBindingKey struct{}

// WithExternalBinding records a server-verified private channel binding.
func WithExternalBinding(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, externalBindingKey{}, id)
}

// ExternalBinding returns the server-owned external-origin fence, if present.
func ExternalBinding(ctx context.Context) string {
	id, ok := ctx.Value(externalBindingKey{}).(string)
	if !ok {
		return ""
	}
	return id
}

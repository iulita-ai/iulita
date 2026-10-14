package main

import (
	"context"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/httpfetch"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
)

// standingGrantAdapter adapts the SQLite store to the engine's narrow
// StandingGrantStore interface.
type standingGrantAdapter struct {
	store *sqlite.Store
}

// NewStandingGrantAdapter wraps a store.
func NewStandingGrantAdapter(s *sqlite.Store) httpfetch.StandingGrantStore {
	return &standingGrantAdapter{store: s}
}

// HasStandingGrant reports whether an unrevoked, unexpired grant covers the
// origin (or globally) for the scope's user.
func (a *standingGrantAdapter) HasStandingGrant(ctx context.Context, scope httpfetch.Scope, origin httpfetch.Origin) (bool, error) {
	if scope.UserID == "" {
		return false, nil
	}
	return a.store.HasHTTPStandingGrant(ctx, scope.UserID, origin.Scheme, origin.Host, origin.Port)
}

// SaveStandingGrant persists a grant issued by the owner's standing choice.
func (a *standingGrantAdapter) SaveStandingGrant(ctx context.Context, scope httpfetch.Scope, origin httpfetch.Origin, standing string, expiresAt time.Time) error {
	g := &domain.HTTPStandingGrant{
		OwnerUserID: scope.UserID,
		Reason:      "mutation",
		ExpiresAt:   expiresAt,
	}
	if standing == "global" {
		g.Scope = domain.HTTPStandingScopeGlobal
	} else {
		g.Scope = domain.HTTPStandingScopeOrigin
		g.Scheme = origin.Scheme
		g.Host = origin.Host
		g.Port = origin.Port
	}
	return a.store.SaveHTTPStandingGrant(ctx, g)
}

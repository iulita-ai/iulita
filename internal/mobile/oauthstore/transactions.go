package oauthstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/oauth2"
)

type requestKey struct{}
type hookKey struct{}
type requestTx struct {
	store          *Store
	tx             *sql.Tx
	savepoint      int
	revocations    map[string]bool
	rotated        map[string]uint64
	verifiedFamily string
	verifiedClient string
}
type hook struct {
	root *requestTx
	name string
	done bool
}

func (s *Store) root(ctx context.Context) (*requestTx, error) {
	r, ok := ctx.Value(requestKey{}).(*requestTx)
	if !ok || r.store != s {
		return nil, ErrTransaction
	}
	return r, nil
}
func (s *Store) transaction(ctx context.Context) (*sql.Tx, error) {
	r, err := s.root(ctx)
	if err != nil {
		return nil, err
	}
	return r.tx, nil
}

// WithRequest is the outer transaction boundary, spanning ALL Fosite phases.
// Callback contexts must not be retained, shared concurrently or used for other
// stores. A Fosite core Commit only releases its savepoint, never this transaction.
func (s *Store) WithRequest(ctx context.Context, fn func(context.Context) error) error {
	if ctx.Value(requestKey{}) != nil {
		return ErrTransaction
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	var kid string
	var epoch uint64
	var binding []byte
	if err = tx.QueryRowContext(ctx, `SELECT kid,signing_epoch,binding FROM oauth_realm WHERE id=?`, s.realm.id).Scan(&kid, &epoch, &binding); err != nil || kid != s.realm.kid || epoch != s.realm.epoch || !equalHash(binding, s.realm.binding()) {
		return ErrKeys
	}
	root := &requestTx{store: s, tx: tx, revocations: map[string]bool{}, rotated: map[string]uint64{}}
	err = fn(context.WithValue(ctx, requestKey{}, root))
	if err == nil {
		return tx.Commit()
	}
	if e := tx.Rollback(); e != nil && !errors.Is(e, sql.ErrTxDone) {
		return e
	}
	// A rejected grant must not commit token/PKCE writes. Only revocations of
	// cryptographically verified, client-bound replays survive the rollback.
	if len(root.revocations) > 0 {
		// Security revocation must not disappear just because the transport
		// disconnected. Keep this bounded DB cleanup independent of that cancel.
		cleanupBase, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		cleanup, e := s.db.BeginTx(cleanupBase, nil)
		if e != nil {
			return e
		}
		defer cleanup.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
		cleanupCtx := context.WithValue(cleanupBase, requestKey{}, &requestTx{store: s, tx: cleanup})
		for id := range root.revocations {
			if checkErr1 := s.revoke(cleanupCtx, id); checkErr1 != nil {
				return checkErr1
			}
		}
		if checkErr2 := cleanup.Commit(); checkErr2 != nil {
			return checkErr2
		}
	}
	return err
}

// BeginTX opens a nested Fosite transaction inside the request boundary.
func (s *Store) BeginTX(ctx context.Context) (context.Context, error) {
	r, err := s.root(ctx)
	if err != nil {
		return nil, err
	}
	r.savepoint++
	name := fmt.Sprintf("fosite_core_%d", r.savepoint)
	if _, err = r.tx.ExecContext(ctx, `SAVEPOINT `+name); err != nil {
		return nil, err
	}
	return context.WithValue(ctx, hookKey{}, &hook{root: r, name: name}), nil
}

// Commit commits the transaction boundary owned by this operation.
func (s *Store) Commit(ctx context.Context) error {
	r, err := s.root(ctx)
	h, ok := ctx.Value(hookKey{}).(*hook)
	if err != nil || !ok || h.root != r || h.done {
		return ErrTransaction
	}
	_, err = r.tx.ExecContext(ctx, `RELEASE SAVEPOINT `+h.name)
	if err == nil {
		h.done = true
	}
	return err
}

// Rollback rolls back the transaction boundary owned by this operation.
func (s *Store) Rollback(ctx context.Context) error {
	r, err := s.root(ctx)
	h, ok := ctx.Value(hookKey{}).(*hook)
	if err != nil || !ok || h.root != r {
		return ErrTransaction
	}
	if h.done {
		return nil
	}
	if _, err = r.tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT `+h.name); err != nil {
		return err
	}
	_, err = r.tx.ExecContext(ctx, `RELEASE SAVEPOINT `+h.name)
	if err == nil {
		h.done = true
	}
	return err
}

// TokenRequest coordinates persistence for BE14/BE15; it is not an HTTP token
// endpoint. Bind the raw code/refresh to its client BEFORE Fosite's replay path
// (which otherwise revokes before those checks), including inactive generations.
// Actual code-verifier/PKCE protocol checks remain Fosite's responsibility.
func (s *Store) TokenRequest(ctx context.Context, grantType, raw, clientID, redirect string, strategy oauth2.CoreStrategy, fn func(context.Context) error) error {
	if strategy == nil || raw == "" || clientID == "" {
		return fosite.ErrInvalidGrant
	}
	return s.WithRequest(ctx, func(ctx context.Context) error {
		var kind, sig string
		switch grantType {
		case "authorization_code":
			kind = "code"
			sig = strategy.AuthorizeCodeSignature(ctx, raw)
		case "refresh_token":
			kind = "refresh"
			sig = strategy.RefreshTokenSignature(ctx, raw)
		default:
			return fosite.ErrUnsupportedGrantType
		}
		req, _, _, err := s.load(ctx, kind, sig)
		if err != nil {
			return fosite.ErrInvalidGrant
		}
		if req.GetClient().GetID() != clientID {
			return fosite.ErrInvalidGrant
		}
		if kind == "code" {
			if req.GetRequestForm().Get("redirect_uri") != redirect {
				return fosite.ErrInvalidGrant
			}
			err = strategy.ValidateAuthorizeCode(ctx, req, raw)
		} else {
			// This preflight proves cryptographic/client binding even for retained
			// expired tokens. Grant eligibility still uses the ORIGINAL expiry in
			// get/Fosite; only a proven replay may reach persistent revocation.
			cryptoRequest := *req
			cryptoRequest.Session = req.GetSession().Clone()
			cryptoRequest.Session.SetExpiresAt(fosite.RefreshToken, time.Now().Add(time.Minute))
			err = strategy.ValidateRefreshToken(ctx, &cryptoRequest, raw)
		}
		if err != nil {
			return fosite.ErrInvalidGrant
		}
		root, operationErr1 := s.root(ctx)
		if operationErr1 != nil {
			return operationErr1
		}
		family, familyErr := s.familyIn(ctx, req.GetID())
		if familyErr != nil || family.ClientID != clientID {
			return fosite.ErrInvalidGrant
		}
		if checkErr := checkClientOwnerIn(ctx, root.tx, clientID, family.OwnerID); checkErr != nil {
			return checkErr
		}
		root.verifiedFamily = req.GetID()
		root.verifiedClient = req.GetClient().GetID()
		err = fn(ctx)
		if errors.Is(err, fosite.ErrSerializationFailure) {
			root.revocations[req.GetID()] = true
			return fosite.ErrInvalidGrant.WithHint("Start a fresh authorization with PKCE.")
		}
		return err
	})
}

// Transaction returns the transaction owned by the current guarded request.
func (s *Store) Transaction(ctx context.Context) (*sql.Tx, error) { return s.transaction(ctx) }

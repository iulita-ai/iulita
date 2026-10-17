package oauthstore

import (
	"context"
	"database/sql"
	"errors"
)

// checkClientOwnerIn binds browser and family authority to immutable operator
// policy in the same transaction. Empty native ownership preserves the original
// multi-account registration; an assigned native client never grants another
// owner access, including through a stale browser flow or credential family.
func checkClientOwnerIn(ctx context.Context, tx *sql.Tx, client, owner string) error {
	var kind, assigned string
	var disabled bool
	err := tx.QueryRowContext(ctx, `SELECT kind,owner_id,disabled FROM oauth_client_policy WHERE client_id=?`, client).Scan(&kind, &assigned, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInvalid
	}
	if err != nil {
		return err
	}
	if disabled || (kind != "native" && kind != "cli") || (assigned != "" && (kind != "native" || assigned != owner)) {
		return ErrInvalid
	}
	return nil
}

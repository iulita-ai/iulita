package oauthstore

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type deviceCursor struct {
	Owner, After string
	Expires      int64
}

// ListDevices returns an owner-scoped device page.
func (s *Store) ListDevices(ctx context.Context, owner string, limit int, page string) ([]Device, string, error) {
	if !canonicalID(owner) || limit < 1 || limit > 100 {
		return nil, "", ErrInvalid
	}
	signer, operationErr1 := s.realm.CursorSigner()
	if operationErr1 != nil {
		return nil, "", operationErr1
	}
	c := deviceCursor{Owner: owner, Expires: time.Now().Add(10 * time.Minute).Unix()}
	if page != "" {
		if signer.Open("device.page.v1", page, &c) != nil || c.Owner != owner || c.Expires <= time.Now().Unix() {
			return nil, "", ErrInvalid
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,owner_id,thumbprint,epoch,revoked,activated,label,pending_expires_at FROM oauth_devices WHERE owner_id=? AND id>? ORDER BY id LIMIT ?`, owner, c.After, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close() //nolint:errcheck // Cleanup only; preserve the operation result.
	var out []Device
	for rows.Next() {
		var d Device
		if checkErr1 := rows.Scan(&d.ID, &d.OwnerID, &d.KeyThumbprint, &d.Epoch, &d.Revoked, &d.Activated, &d.Label, &d.PendingExpiresAt); checkErr1 != nil {
			return nil, "", checkErr1
		}
		out = append(out, d)
	}
	if checkErr2 := rows.Err(); checkErr2 != nil {
		return nil, "", checkErr2
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		c.After = out[len(out)-1].ID
		next, err = signer.Seal("device.page.v1", c)
	}
	return out, next, err
}

// RevokeDeviceOperation commits the idempotency key and revocation together.
// Rebinding an operation to a second device cannot trigger a second effect.
func (s *Store) RevokeDeviceOperation(ctx context.Context, owner, client, operation, device string) error {
	if !canonicalID(owner) || !canonicalID(operation) || !canonicalID(device) || client == "" {
		return ErrInvalid
	}
	return s.WithRequest(ctx, func(ctx context.Context) error {
		tx, operationErr2 := s.transaction(ctx)
		if operationErr2 != nil {
			return operationErr2
		}
		var prior string
		err := tx.QueryRowContext(ctx, `SELECT device_id FROM oauth_device_revocations WHERE owner_id=? AND client_id=? AND operation_id=?`, owner, client, operation).Scan(&prior)
		if err == nil {
			if prior != device {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		// Compact bodyless tombstones cannot be discarded or turn into a
		// lifetime quota which would eventually prevent security revocation.
		var epoch uint64
		var revoked bool
		if err = tx.QueryRowContext(ctx, `SELECT epoch,revoked FROM oauth_devices WHERE owner_id=? AND id=?`, owner, device).Scan(&epoch, &revoked); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if !revoked {
			if checkErr3 := s.revokeDeviceIn(ctx, owner, device, epoch); checkErr3 != nil {
				return checkErr3
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO oauth_device_revocations VALUES(?,?,?,?)`, owner, client, operation, device)
		return err
	})
}

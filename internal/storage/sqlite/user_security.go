package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/iulita-ai/iulita/internal/domain"
)

// WithUserSecurity holds a short write reservation in the account authority
// while another realm verifies/mints credentials. WAL read snapshots alone do
// not prevent a concurrent password/disable/delete commit. Never use this for
// streams, network/provider calls or waiting for a user decision.
func (s *Store) WithUserSecurity(ctx context.Context, id string, fn func(*domain.User) error) error {
	// Queue local reservations before opening competing SQLite writers. SQLite's
	// busy handler is not fair and may sleep through context cancellation; a
	// validation burst must not starve Control receipts or revocation checks.
	// The real BEGIN IMMEDIATE below remains the cross-process authority fence.
	if err := s.securityReservations.Acquire(ctx, 1); err != nil {
		return err
	}
	defer s.securityReservations.Release(1)
	conn, err := s.db.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close() //nolint:errcheck // Cleanup only; preserve the operation result.
	if _, err = conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), `ROLLBACK`) //nolint:errcheck // Roll back on failure; a successful explicit COMMIT already ends the transaction.
	u := new(domain.User)
	if err = s.db.NewSelect().Model(u).Conn(conn).Where("id = ?", id).Scan(ctx); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fn(nil)
		}
		return err
	}
	if checkErr1 := fn(u); checkErr1 != nil {
		return checkErr1
	}
	_, err = conn.ExecContext(ctx, `COMMIT`)
	return err
}

// RevokeUserSessions is an account-authority security logout. Mobile validation
// and business fences observe this durable epoch; it does not claim remote wipe.
func (s *Store) RevokeUserSessions(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE users SET security_epoch=security_epoch+1 WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, operationErr1 := result.RowsAffected()
	if operationErr1 != nil {
		return operationErr1
	}
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}

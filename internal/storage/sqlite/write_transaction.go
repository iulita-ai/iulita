package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"
)

// runWriteTransaction reserves the SQLite writer before reading authority.
// Callbacks contain only short database operations, run once, and use this IDB
// for every query. Provider calls, user prompts and nested transactions do not
// belong here. Other transactions keep their normal database/sql behavior.
func (s *Store) runWriteTransaction(ctx context.Context, fn func(context.Context, bun.IDB) error) (err error) {
	// Queue local callers without occupying pooled connections or SQLite's
	// busy handler. BEGIN IMMEDIATE remains the cross-process authority fence.
	if acquireErr := s.writeReservations.Acquire(ctx, 1); acquireErr != nil {
		return acquireErr
	}
	defer s.writeReservations.Release(1)
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	attempted, committed := false, false
	defer func() {
		if attempted && !committed {
			// A cancelled caller must not return an active transaction to the pool.
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			_, rollbackErr := conn.ExecContext(cleanupCtx, "ROLLBACK")
			cancel()
			if rollbackErr != nil {
				// Raw BEGIN is outside sql.Tx's cleanup. If its final state cannot
				// be established, physically discard the connection, not just its lease.
				discardErr := conn.Raw(func(any) error { return driver.ErrBadConn })
				if discardErr != nil && !errors.Is(discardErr, driver.ErrBadConn) && !errors.Is(discardErr, sql.ErrConnDone) {
					err = errors.Join(err, fmt.Errorf("discarding uncertain SQLite connection: %w", discardErr))
				}
				err = errors.Join(err, fmt.Errorf("rolling back SQLite write: %w", rollbackErr))
			}
		}
		if closeErr := conn.Close(); closeErr != nil && !errors.Is(closeErr, sql.ErrConnDone) {
			err = errors.Join(err, fmt.Errorf("closing SQLite write connection: %w", closeErr))
		}
	}()
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	// Even a failed/cancelled BEGIN attempt is cleaned up conservatively.
	attempted = true
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("reserving SQLite writer: %w", err)
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if callbackErr := fn(ctx, conn); callbackErr != nil {
		return callbackErr
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("committing SQLite write: %w", err)
	}
	committed = true
	return nil
}

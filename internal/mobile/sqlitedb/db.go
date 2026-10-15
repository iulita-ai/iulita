// Package sqlitedb owns the connection policy for mobile persistence. It does
// not alter the legacy connection pool or enable any transport.
package sqlitedb

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"

	"github.com/uptrace/bun/driver/sqliteshim"
)

// Open opens the store with its required schema and safety settings.
func Open(ctx context.Context, path string, create bool) (*sql.DB, error) {
	return open(ctx, path, create, false)
}

// OpenReadOnly reserves a bounded WAL reader pool that cannot be consumed by
// waiting BEGIN IMMEDIATE writers or perform writes through another connection.
func OpenReadOnly(ctx context.Context, path string) (*sql.DB, error) {
	return open(ctx, path, false, true)
}

func open(ctx context.Context, path string, create, readOnly bool) (*sql.DB, error) {
	if path == "" || path == ":memory:" {
		return nil, errors.New("mobile storage requires a persistent path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: abs}
	q := u.Query()
	q.Set("mode", "rw")
	if create {
		q.Set("mode", "rwc")
	}
	if readOnly {
		q.Set("mode", "ro")
		q.Add("_pragma", "query_only(1)")
	} else {
		q.Set("_txlock", "immediate")
	}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(FULL)")
	u.RawQuery = q.Encode()
	db, err := sql.Open(sqliteshim.ShimName, u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if readOnly {
		if err = db.PingContext(ctx); err != nil {
			db.Close() //nolint:errcheck,gosec // Preserve the reader initialization error.
			return nil, err
		}
		return db, nil
	}
	if _, err = db.ExecContext(ctx, `PRAGMA journal_mode=WAL`); err != nil {
		db.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
		return nil, err
	}
	return db, nil
}

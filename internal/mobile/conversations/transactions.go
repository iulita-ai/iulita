package conversations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"time"
)

type transactionKey struct{}
type repoRequest struct {
	repository *Repository
	tx         *sql.Tx
	done       bool
}
type repoTx struct {
	*sql.Tx
	owns bool
}

// Commit commits the transaction boundary owned by this operation.
func (t *repoTx) Commit() error {
	if !t.owns {
		return nil
	}
	return t.Tx.Commit()
}

// Rollback rolls back the transaction boundary owned by this operation.
func (t *repoTx) Rollback() error {
	if !t.owns {
		return nil
	}
	return t.Tx.Rollback()
}
func (r *Repository) begin(ctx context.Context) (*repoTx, error) {
	if root, ok := ctx.Value(transactionKey{}).(*repoRequest); ok {
		if root.repository != r || root.done {
			return nil, ErrConflict
		}
		return &repoTx{Tx: root.tx}, nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if err = verify(ctx); err != nil {
		tx.Rollback() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
		return nil, err
	}
	return &repoTx{tx, true}, nil
}

// Operation commits the mutation and its response in the SAME transaction.
// payload must be a canonical semantic encoding, without transport credentials.
// Callbacks are synchronous and must not perform external effects or retain ctx.
func (r *Repository) Operation(ctx context.Context, owner, client, method, id string, payload []byte, fn func(context.Context) ([]byte, error), scope ...string) (responseBody []byte, wasReplayed bool, returnErr error) {
	if !validID(owner) || !validID(id) || client == "" || method == "" || len(payload) > 2<<20 {
		return nil, false, ErrInvalid
	}
	if ctx.Value(transactionKey{}) != nil {
		return nil, false, ErrConflict
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	if checkErr1 := verify(ctx); checkErr1 != nil {
		return nil, false, checkErr1
	}
	root := &repoRequest{repository: r, tx: tx}
	defer func() { root.done = true }()
	digest := sha256.Sum256(payload)
	// Bodies are a bounded cache; compact effect/digest tombstones never expire.
	if _, err = tx.ExecContext(ctx, `UPDATE mobile_operations SET response=X'',redacted=1 WHERE owner_id=? AND redacted=0 AND created_at<?`, owner, time.Now().Add(-7*24*time.Hour).Unix()); err != nil {
		return nil, false, err
	}
	var old, response []byte
	var redacted bool
	err = tx.QueryRowContext(ctx, `SELECT digest,response,redacted FROM mobile_operations WHERE owner_id=? AND client_id=? AND method=? AND id=?`, owner, client, method, id).Scan(&old, &response, &redacted)
	if err == nil {
		if !bytes.Equal(old, digest[:]) {
			return nil, false, ErrIdempotency
		}
		if redacted {
			return nil, false, ErrResync
		}
		return response, true, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	var count, cachedBytes int
	if checkErr2 := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(response)),0) FROM mobile_operations WHERE owner_id=? AND redacted=0`, owner).Scan(&count, &cachedBytes); checkErr2 != nil {
		return nil, false, checkErr2
	}
	safety := method == "ClearHistory" || method == "CancelTurn"
	full := count >= 10000 || cachedBytes >= 16<<20
	if full && !safety {
		return nil, false, ErrQueueFull
	}
	response, err = fn(context.WithValue(ctx, transactionKey{}, root))
	if err != nil {
		return nil, false, err
	}
	if len(response) > 2<<20 {
		return nil, false, ErrInvalid
	}
	if cachedBytes+len(response) > 16<<20 {
		if !safety {
			return nil, false, ErrQueueFull
		}
		full = true
	}
	stored := response
	redacted = false
	if full && safety {
		stored = []byte{}
		redacted = true
	}
	conversation := ""
	if len(scope) > 0 {
		conversation = scope[0]
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO mobile_operations(owner_id,client_id,method,id,digest,response,created_at,conversation_id,redacted) VALUES(?,?,?,?,?,?,?,?,?)`, owner, client, method, id, digest[:], stored, time.Now().Unix(), conversation, redacted)
	if err != nil {
		return nil, false, err
	}
	return response, false, tx.Commit()
}

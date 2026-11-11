package conversations

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iulita-ai/iulita/internal/mobile/sqlitedb"
)

func TestWriterAdmissionCancellationDoesNotConsumePoolOrRunCallback(t *testing.T) {
	r, _, owner, _, _ := fixture(t)
	if err := r.writers.Acquire(ctx, 1); err != nil {
		t.Fatal(err)
	}
	defer r.writers.Release(1)
	// Exhaust only the writer pool. Queued callers must not reach that pool;
	// WAL authority reads retain their independent connections.
	held := make([]*sql.Conn, 0, r.db.Stats().MaxOpenConnections)
	defer func() {
		for _, conn := range held {
			conn.Close()
		}
	}()
	for range r.db.Stats().MaxOpenConnections {
		conn, err := r.db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, conn)
	}
	before := r.db.Stats().WaitCount
	for _, operation := range []string{"begin", "operation", "metadata"} {
		t.Run(operation, func(t *testing.T) {
			waiting, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
			defer cancel()
			var callbacks atomic.Int64
			var err error
			switch operation {
			case "begin":
				_, err = r.begin(WithVerifier(waiting, func(context.Context) error { callbacks.Add(1); return nil }))
			case "operation":
				_, _, err = r.Operation(waiting, owner, "client", "fixture", uuid.NewString(), nil, func(context.Context) ([]byte, error) { callbacks.Add(1); return nil, nil })
			case "metadata":
				err = r.execWriter(waiting, `UPDATE mobile_runtime SET expires_at=0 WHERE id=1`)
			}
			if !errors.Is(err, context.DeadlineExceeded) || callbacks.Load() != 0 {
				t.Fatal("queued cancellation ran an operation", err, callbacks.Load())
			}
			if r.db.Stats().WaitCount != before {
				t.Fatal("queued writer occupied the database pool")
			}
		})
	}
}

func TestLocalWriterQueueOutlastsSQLiteBusyWindow(t *testing.T) {
	r, _, _, _, _ := fixture(t)
	// Shorten only this fixture's native busy window. Every currently pooled
	// connection is configured before the held writer starts.
	connections := make([]*sql.Conn, 0, r.db.Stats().MaxOpenConnections)
	for range r.db.Stats().MaxOpenConnections {
		conn, err := r.db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = conn.ExecContext(ctx, `PRAGMA busy_timeout=25`); err != nil {
			t.Fatal(err)
		}
		connections = append(connections, conn)
	}
	for _, conn := range connections {
		conn.Close()
	}
	holder, err := r.begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback()
	// A direct pooled BEGIN demonstrates the exact failure avoided by admission.
	direct, err := r.db.BeginTx(ctx, nil)
	if err == nil {
		direct.Rollback()
		t.Fatal("fixture did not hold the SQLite writer")
	}
	var coded interface{ Code() int }
	if !errors.As(err, &coded) || coded.Code()&0xff != 5 {
		t.Fatal("fixture did not reproduce SQLITE_BUSY", err)
	}
	done := make(chan error, 1)
	waiting, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	go func() { done <- r.ReconcileSecurity(waiting) }()
	select {
	case err = <-done:
		t.Fatal("local writer entered SQLite instead of waiting", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err = holder.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal("queued reconciliation failed after writer release", err)
	}
}

func TestWriterOwnershipReleasesExactlyOnceAndRechecksVerifier(t *testing.T) {
	r, _, owner, _, _ := fixture(t)
	outer, err := r.begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer outer.Rollback()
	root := &repoRequest{repository: r, tx: outer.Tx}
	nestedContext := context.WithValue(ctx, transactionKey{}, root)
	nested, err := r.begin(nestedContext)
	if err != nil {
		t.Fatal(err)
	}
	if err = nested.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = r.execWriter(nestedContext, `UPDATE mobile_runtime SET expires_at=0 WHERE id=1`); err != nil {
		t.Fatal("nested metadata reacquired its writer", err)
	}
	if r.writers.TryAcquire(1) {
		r.writers.Release(1)
		t.Fatal("nested commit released the outer reservation")
	}
	root.done = true
	if err = outer.Commit(); err != nil {
		t.Fatal(err)
	}
	second, err := r.begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Rollback()
	_ = outer.Rollback() // The common defer-after-Commit pattern must be harmless.
	if r.writers.TryAcquire(1) {
		r.writers.Release(1)
		t.Fatal("duplicate cleanup released a later writer")
	}
	if err = second.Rollback(); err != nil {
		t.Fatal(err)
	}
	denied := errors.New("revoked fixture authority")
	if _, err = r.begin(WithVerifier(ctx, func(context.Context) error { return denied })); !errors.Is(err, denied) {
		t.Fatal("verifier was bypassed", err)
	}
	calls, verified := 0, 0
	verifiedContext := WithVerifier(ctx, func(context.Context) error { verified++; return nil })
	if _, _, err = r.Operation(verifiedContext, owner, "client", "fixture", uuid.NewString(), nil, func(context.Context) ([]byte, error) { calls++; return []byte("ok"), nil }); err != nil || calls != 1 || verified != 1 {
		t.Fatal("writer leaked or verifier/callback repeated", err, calls, verified)
	}
}

func TestWriterAdmissionFailureAndVerifierPanicReleaseQueue(t *testing.T) {
	t.Run("begin_failure", func(t *testing.T) {
		r, _, _, _, _ := fixture(t)
		if err := r.db.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := r.begin(ctx); err == nil {
			t.Fatal("closed database admitted a transaction")
		}
		if !r.writers.TryAcquire(1) {
			t.Fatal("failed BEGIN leaked writer admission")
		}
		r.writers.Release(1)
	})
	t.Run("verifier_panic", func(t *testing.T) {
		r, _, _, _, _ := fixture(t)
		caught := false
		func() {
			defer func() { caught = recover() == "fixture verifier panic" }()
			_, _ = r.begin(WithVerifier(ctx, func(context.Context) error { panic("fixture verifier panic") }))
		}()
		if !caught {
			t.Fatal("verifier panic was changed or suppressed")
		}
		waiting, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		next, err := r.begin(waiting)
		if err != nil {
			t.Fatal("panic leaked writer or transaction", err)
		}
		if err = next.Rollback(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestFailedCommitReleasesQueueWithoutReplayingTransaction(t *testing.T) {
	r, _, _, _, _ := fixture(t)
	for _, query := range []string{`CREATE TABLE fixture_parent(id INTEGER PRIMARY KEY)`, `CREATE TABLE fixture_child(id INTEGER REFERENCES fixture_parent(id) DEFERRABLE INITIALLY DEFERRED)`} {
		if err := r.execWriter(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	writer, err := r.begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	if _, err = writer.ExecContext(ctx, `INSERT INTO fixture_child VALUES(123)`); err != nil {
		t.Fatal(err)
	}
	if err = writer.Commit(); err == nil {
		t.Fatal("expected deferred constraint failure at COMMIT")
	}
	waiting, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	next, err := r.begin(waiting)
	if err != nil {
		t.Fatal("failed COMMIT leaked writer admission", err)
	}
	defer next.Rollback()
	var rows int
	if err = next.QueryRowContext(ctx, `SELECT COUNT(*) FROM fixture_child`).Scan(&rows); err != nil || rows != 0 {
		t.Fatal("failed transaction was replayed or committed", rows, err)
	}
}

func TestRenewRuntimeCannotReviveLeaseExpiredWhileWaitingForSQLite(t *testing.T) {
	r, path, _, _, _ := fixture(t)
	runtime, err := r.AcquireRuntime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	external, err := sqlitedb.Open(ctx, path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer external.Close()
	holder, err := external.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback()
	expiry := time.Now().Add(time.Second)
	if _, err = holder.ExecContext(ctx, `UPDATE mobile_runtime SET expires_at=? WHERE id=1`, expiry.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	waiting, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.RenewRuntime(waiting, runtime) }()
	// Wait until Renew has taken the local reservation and is waiting on the
	// independent SQLite writer, before the existing lease expires.
	for r.writers.TryAcquire(1) {
		r.writers.Release(1)
		if !time.Now().Before(expiry) {
			t.Fatal("renewal did not reach SQLite before fixture expiry")
		}
		time.Sleep(time.Millisecond)
	}
	<-time.After(time.Until(expiry) + 20*time.Millisecond)
	if err = holder.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = <-done; !errors.Is(err, ErrFenced) {
		t.Fatal("expired coordinator was revived after waiting", err)
	}
	var saved int64
	if err = r.authorityReads.QueryRowContext(ctx, `SELECT expires_at FROM mobile_runtime WHERE id=1`).Scan(&saved); err != nil || saved != expiry.UnixMilli() {
		t.Fatal("failed renewal changed persisted expiry", saved, err)
	}
}

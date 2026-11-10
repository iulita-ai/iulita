package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/driver/sqliteshim"
)

// This separate process deliberately bypasses any in-process write mutex.
func TestTopicCompetingWriterProcess(t *testing.T) {
	path := os.Getenv("IULITA_SQLITE_COMPETING_TEST_DB")
	if path == "" {
		t.Skip("subprocess entry point")
	}
	db, err := sql.Open(sqliteshim.ShimName, "file:"+path+"?mode=rw&_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.ExecContext(context.Background(), "INSERT INTO competing_writes (value) VALUES ('independent writer')"); err != nil {
		var coded interface{ Code() int }
		if errors.As(err, &coded) && coded.Code()&255 == 5 {
			fmt.Println("COMPETING_WRITER_BUSY")
			return
		}
		t.Fatal(err)
	}
	fmt.Println("COMPETING_WRITER_COMMITTED")
}

func competingWriter(t *testing.T, path string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTopicCompetingWriterProcess$")
	cmd.Env = append(os.Environ(), "IULITA_SQLITE_COMPETING_TEST_DB="+path)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

type messageInsertHook struct {
	once   sync.Once
	before func()
}

func (h *messageInsertHook) BeforeQuery(ctx context.Context, event *bun.QueryEvent) context.Context {
	if strings.HasPrefix(event.Query, `INSERT INTO "chat_messages"`) {
		h.once.Do(h.before)
	}
	return ctx
}

func (*messageInsertHook) AfterQuery(context.Context, *bun.QueryEvent) {}

func TestTopicAssistantHistorySurvivesIndependentWriter(t *testing.T) {
	s, scope := deletionStore(t)
	ctx := context.Background()
	var path string
	if err := s.db.NewRaw("SELECT file FROM pragma_database_list WHERE name='main'").Scan(ctx, &path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "CREATE TABLE competing_writes (value TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveMessage(ctx, &domain.ChatMessage{ChatID: scope.ConversationID, UserID: scope.OwnerID, Role: domain.RoleUser, Content: "Synthetic topic question"}); err != nil {
		t.Fatal(err)
	}
	var peerOutput string
	var peerErr error
	s.db.AddQueryHook(&messageInsertHook{before: func() {
		// Run after lifecycle reads but immediately before the actual INSERT.
		// Deferred transactions let this writer commit and invalidate their snapshot.
		peerOutput, peerErr = competingWriter(t, path)
	}})
	err := s.SaveMessage(ctx, &domain.ChatMessage{ChatID: scope.ConversationID, UserID: scope.OwnerID, Role: domain.RoleAssistant, Content: "Synthetic persisted answer"})
	if peerErr != nil {
		t.Fatalf("competing process: %v: %s", peerErr, peerOutput)
	}
	if err != nil {
		var coded interface{ Code() int }
		if errors.As(err, &coded) {
			t.Logf("SQLite extended error code: %d", coded.Code())
		}
		t.Fatalf("assistant history lost after competing writer (%s): %v", strings.TrimSpace(peerOutput), err)
	}
	if !strings.Contains(peerOutput, "COMPETING_WRITER_BUSY") {
		t.Fatalf("lifecycle check did not hold a cross-process writer reservation: %s", peerOutput)
	}
	history, err := s.GetHistory(ctx, scope.ConversationID, 10)
	if err != nil || len(history) != 2 || history[0].Role != domain.RoleUser || history[1].Role != domain.RoleAssistant {
		t.Fatalf("incomplete topic history: %+v, %v", history, err)
	}
	out, err := competingWriter(t, path)
	if err != nil || !strings.Contains(out, "COMPETING_WRITER_COMMITTED") {
		t.Fatalf("reservation leaked after successful write: %v: %s", err, out)
	}
}

func TestWriteTransactionRollbackAndConnectionCleanup(t *testing.T) {
	for _, mode := range []string{"callback error", "cancel", "panic", "commit failure", "rollback failure"} {
		t.Run(mode, func(t *testing.T) {
			s, scope := deletionStore(t)
			s.db.SetMaxOpenConns(1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			failure := errors.New("synthetic callback failure")
			calls := 0
			var recovered any
			var err error
			func() {
				defer func() { recovered = recover() }()
				err = s.runWriteTransaction(ctx, func(ctx context.Context, db bun.IDB) error {
					calls++
					if _, e := db.ExecContext(ctx, "CREATE TEMP TABLE connection_marker (id INTEGER)"); e != nil {
						return e
					}
					if mode == "rollback failure" {
						if _, e := db.ExecContext(ctx, "COMMIT"); e != nil {
							return e
						}
						return failure // Inject an uncertain cleanup state.
					}
					if _, e := db.NewInsert().Model(&domain.ChatMessage{ChatID: scope.ConversationID, UserID: scope.OwnerID, Role: domain.RoleAssistant, Content: "must roll back"}).Exec(ctx); e != nil {
						return e
					}
					switch mode {
					case "cancel":
						cancel()
						return ctx.Err()
					case "panic":
						panic(failure)
					case "commit failure":
						_, e := db.ExecContext(ctx, "ROLLBACK")
						return e // COMMIT must fail; the callback must not run again.
					default:
						return failure
					}
				})
			}()
			if calls != 1 {
				t.Fatalf("callback executed %d times", calls)
			}
			if mode == "panic" {
				if recoveredErr, ok := recovered.(error); !ok || !errors.Is(recoveredErr, failure) {
					t.Fatalf("panic changed: %v", recovered)
				}
			} else if err == nil || recovered != nil {
				t.Fatalf("failure lost: error=%v panic=%v", err, recovered)
			}
			if (mode == "callback error" || mode == "rollback failure") && !errors.Is(err, failure) {
				t.Fatalf("original callback error lost: %v", err)
			}
			var markers int
			if e := s.db.NewRaw("SELECT count(*) FROM sqlite_temp_master WHERE name='connection_marker'").Scan(context.Background(), &markers); e != nil || markers != 0 {
				t.Fatalf("unclean connection reused: marker=%d error=%v", markers, e)
			}
			history, e := s.GetHistory(context.Background(), scope.ConversationID, 0)
			if e != nil || len(history) != 0 {
				t.Fatalf("rolled-back write survived: %+v, %v", history, e)
			}
			if e = s.SaveMessage(context.Background(), &domain.ChatMessage{ChatID: scope.ConversationID, Role: domain.RoleAssistant, Content: "next write"}); e != nil {
				t.Fatal("reservation leaked", e)
			}
		})
	}
}

func TestWaitingWriteReservationHonorsCancellation(t *testing.T) {
	s, _ := deletionStore(t)
	ctx := context.Background()
	if err := s.writeReservations.Acquire(ctx, 1); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	calls := 0
	err := s.runWriteTransaction(waitCtx, func(context.Context, bun.IDB) error { calls++; return nil })
	s.writeReservations.Release(1)
	if !errors.Is(err, context.DeadlineExceeded) || calls != 0 {
		t.Fatalf("cancelled waiter entered transaction: calls=%d error=%v", calls, err)
	}
	if err := s.runWriteTransaction(ctx, func(context.Context, bun.IDB) error { return nil }); err != nil {
		t.Fatal("cancelled waiter leaked admission", err)
	}
}

type deletionBeginHook struct {
	once    sync.Once
	started chan struct{}
}

func (h *deletionBeginHook) BeforeQuery(ctx context.Context, event *bun.QueryEvent) context.Context {
	if strings.HasPrefix(event.Query, "BEGIN") {
		h.once.Do(func() { close(h.started) })
	}
	return ctx
}

func (*deletionBeginHook) AfterQuery(context.Context, *bun.QueryEvent) {}

func TestReservedWritePrecedesIndependentDeletionWithoutResurrection(t *testing.T) {
	s, scope := deletionStore(t)
	var path string
	if err := s.db.NewRaw("SELECT file FROM pragma_database_list WHERE name='main'").Scan(context.Background(), &path); err != nil {
		t.Fatal(err)
	}
	peer, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	started := make(chan struct{})
	peer.db.AddQueryHook(&deletionBeginHook{started: started})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	held, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	written := make(chan error, 1)
	go func() {
		written <- s.withConversationWrite(ctx, scope.ConversationID, func(db bun.IDB) error {
			if _, insertErr := db.NewInsert().Model(&domain.ChatMessage{ChatID: scope.ConversationID, UserID: scope.OwnerID, Role: domain.RoleAssistant, Content: "synthetic answer before deletion"}).Exec(ctx); insertErr != nil {
				return insertErr
			}
			close(held)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-held:
	case <-ctx.Done():
		t.Fatal("writer never acquired its reservation")
	}
	deleted := make(chan error, 1)
	go func() {
		_, deletionErr := peer.BeginTelegramTopicDeletion(ctx, scope, "manual_sync")
		deleted <- deletionErr
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("independent deletion did not attempt admission")
	}
	unblock()
	if err = <-written; err != nil {
		t.Fatal(err)
	}
	if err = <-deleted; err != nil {
		t.Fatal(err)
	}
	if err = s.SaveMessage(ctx, &domain.ChatMessage{ChatID: scope.ConversationID, UserID: scope.OwnerID, Role: domain.RoleAssistant, Content: "forbidden late answer"}); !errors.Is(err, domain.ErrConversationDeleted) {
		t.Fatal("deletion fence lost", err)
	}
	if history, readErr := s.GetHistory(ctx, scope.ConversationID, 0); readErr != nil || len(history) != 0 {
		t.Fatalf("deleted conversation resurrected: %+v, %v", history, readErr)
	}
}

type cancelAfterBeginHook struct {
	once   sync.Once
	cancel context.CancelFunc
}

func (*cancelAfterBeginHook) BeforeQuery(ctx context.Context, _ *bun.QueryEvent) context.Context {
	return ctx
}

func (h *cancelAfterBeginHook) AfterQuery(_ context.Context, event *bun.QueryEvent) {
	if event.Query == "BEGIN IMMEDIATE" && event.Err == nil {
		h.once.Do(h.cancel)
	}
}

func TestCancelledAfterWriteBeginSkipsCallbackAndReleasesConnection(t *testing.T) {
	s, scope := deletionStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.db.AddQueryHook(&cancelAfterBeginHook{cancel: cancel})
	calls := 0
	err := s.runWriteTransaction(ctx, func(context.Context, bun.IDB) error { calls++; return nil })
	if !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("callback admitted after cancellation: calls=%d error=%v", calls, err)
	}
	if err = s.SaveMessage(context.Background(), &domain.ChatMessage{ChatID: scope.ConversationID, Role: domain.RoleAssistant, Content: "healthy after cancellation"}); err != nil {
		t.Fatal("cancelled transaction leaked", err)
	}
}

func TestSQLiteWriterWaitCancellationDoesNotRunCallback(t *testing.T) {
	s, scope := deletionStore(t)
	ctx := context.Background()
	s.db.SetMaxOpenConns(1)
	if _, err := s.db.ExecContext(ctx, "PRAGMA busy_timeout=100"); err != nil {
		t.Fatal(err)
	}
	var path string
	if err := s.db.NewRaw("SELECT file FROM pragma_database_list WHERE name='main'").Scan(ctx, &path); err != nil {
		t.Fatal(err)
	}
	peer, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	holder, err := peer.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if _, err = holder.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer holder.ExecContext(context.Background(), "ROLLBACK")
	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	calls := 0
	started := time.Now()
	err = s.runWriteTransaction(waitCtx, func(context.Context, bun.IDB) error { calls++; return nil })
	if err == nil || waitCtx.Err() != context.DeadlineExceeded || calls != 0 || time.Since(started) > time.Second {
		t.Fatalf("SQLite wait cancellation failed: calls=%d elapsed=%v error=%v", calls, time.Since(started), err)
	}
	if _, err = holder.ExecContext(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveMessage(ctx, &domain.ChatMessage{ChatID: scope.ConversationID, Role: domain.RoleAssistant, Content: "healthy after peer writer"}); err != nil {
		t.Fatal("writer wait left an unusable connection", err)
	}
}

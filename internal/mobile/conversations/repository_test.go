package conversations

import (
	"context"
	"crypto/rand"
	"errors"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/mobile/securetoken"
	legacy "github.com/iulita-ai/iulita/internal/storage/sqlite"
)

var ctx = context.Background()

func fixture(t *testing.T) (repository *Repository, accountPath, ownerID, clientID string, cursorSigner *securetoken.Signer) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "existing.sqlite")
	old, err := legacy.New(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = old.RunMigrations(ctx); err != nil {
		t.Fatal(err)
	}
	owner, other := uuid.NewString(), uuid.NewString()
	for i, id := range []string{owner, other} {
		if err = old.CreateUser(ctx, &domain.User{ID: id, Username: []string{"owner", "other"}[i], PasswordHash: "unused"}); err != nil {
			t.Fatal(err)
		}
	}
	if err = old.SaveMessage(ctx, &domain.ChatMessage{ChatID: "web:" + owner, UserID: owner, Role: domain.RoleUser, Content: "legacy"}); err != nil {
		t.Fatal(err)
	}
	old.Close()
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		t.Fatal(err)
	}
	signer, _ := securetoken.New(key)
	r, err := OpenExisting(ctx, path, signer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r, path, owner, other, signer
}
func create(t *testing.T, r *Repository, owner string) Conversation {
	t.Helper()
	c, err := r.Create(ctx, owner, "Personal", Settings{ProfileID: "default", MemoryMode: "isolated"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func TestOwnershipMigrationAndRestart(t *testing.T) {
	r, path, owner, other, key := fixture(t)
	c := create(t, r, owner)
	turn := uuid.NewString()
	if err := r.BindTurn(ctx, owner, c.ID, 1, turn, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Append(ctx, owner, c.ID, 1, turn, 1, "user", "private"); err != nil {
		t.Fatal(err)
	}
	title := "renamed"
	foreign := []func() error{
		func() error { _, err := r.Get(ctx, other, c.ID); return err }, func() error { _, err := r.Update(ctx, other, c.ID, 1, &title, nil); return err },
		func() error { _, err := r.Archive(ctx, other, c.ID, 1, true); return err }, func() error { _, err := r.Clear(ctx, other, c.ID, 1); return err },
		func() error { _, _, err := r.Messages(ctx, other, c.ID, 1, 10, ""); return err }, func() error { _, err := r.MarkRead(ctx, other, c.ID, 1, 1); return err },
		func() error { return r.BindTurn(ctx, other, c.ID, 1, uuid.NewString(), 2) }, func() error { _, err := r.Append(ctx, other, c.ID, 1, turn, 1, "assistant", "leak"); return err },
	}
	for _, fn := range foreign {
		if err := fn(); !errors.Is(err, ErrNotFound) {
			t.Fatalf("foreign ID: %v", err)
		}
	}
	list, _, err := r.List(ctx, other, true, 10, "")
	if err != nil || len(list) != 0 {
		t.Fatalf("foreign list: %#v %v", list, err)
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	r, err = OpenExisting(ctx, path, key)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	messages, _, err := r.Messages(ctx, owner, c.ID, 1, 10, "")
	if err != nil || len(messages) != 1 || messages[0].Text != "private" {
		t.Fatalf("restart: %v %v", messages, err)
	}
	old, err := legacy.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	if err = old.RunMigrations(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := old.GetHistory(ctx, "web:"+owner, 10)
	if err != nil || len(rows) != 1 || rows[0].Content != "legacy" {
		t.Fatal("legacy history changed")
	}
	hits, err := old.SearchMessagesByUser(ctx, owner, "private", 10)
	if err != nil || len(hits) != 0 {
		t.Fatal("mobile text entered legacy FTS")
	}
	ids, err := old.GetChatIDs(ctx)
	if err != nil || len(ids) != 1 {
		t.Fatalf("legacy IDs: %v %v", ids, err)
	}
	if _, _, err = r.List(ctx, owner, true, 10, ""); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = r.db.QueryRow(`SELECT COUNT(*) FROM mobile_conversations`).Scan(&n); err != nil || n != 1 {
		t.Fatal("migration imported legacy channels")
	}
}
func TestArchiveClearVersionAndReadPositions(t *testing.T) {
	r, _, owner, _, _ := fixture(t)
	c := create(t, r, owner)
	turn := uuid.NewString()
	if err := r.BindTurn(ctx, owner, c.ID, 1, turn, 1); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"user", "assistant"} {
		if _, err := r.Append(ctx, owner, c.ID, 1, turn, 1, role, "text"); err != nil {
			t.Fatal(err)
		}
	}
	c, _ = r.Get(ctx, owner, c.ID)
	if c.UnreadCount != 1 {
		t.Fatal("unread baseline")
	}
	if _, err := r.MarkRead(ctx, owner, c.ID, 1, 3); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := r.MarkRead(ctx, owner, c.ID, 1, 2); err != nil {
		t.Fatal(err)
	}
	c, err := r.MarkRead(ctx, owner, c.ID, 1, 1)
	if err != nil || c.ReadSeq != 2 || c.UnreadCount != 0 {
		t.Fatalf("monotonic read: %#v %v", c, err)
	}
	c, err = r.Archive(ctx, owner, c.ID, c.Version, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.BindTurn(ctx, owner, c.ID, 1, uuid.NewString(), 2); !errors.Is(err, ErrArchived) {
		t.Fatal(err)
	}
	if _, err = r.Archive(ctx, owner, c.ID, 1, false); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	title := "new title"
	if _, err = r.Update(ctx, owner, c.ID, 1, &title, nil); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	c, err = r.Archive(ctx, owner, c.ID, c.Version, false)
	if err != nil {
		t.Fatal(err)
	}
	c, err = r.Clear(ctx, owner, c.ID, 1)
	if err != nil || c.Generation != 2 || c.ReadSeq != 0 || c.MessageSeq != 0 {
		t.Fatalf("clear: %#v %v", c, err)
	}
	if _, err = r.Clear(ctx, owner, c.ID, 1); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err = r.Append(ctx, owner, c.ID, 1, turn, 1, "assistant", "late"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err = r.BindTurn(ctx, owner, c.ID, 2, turn, 1); err == nil {
		t.Fatal("old turn ID rebound")
	}
	if _, err = r.MarkRead(ctx, owner, c.ID, 1, 1); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	var n int
	if err = r.db.QueryRow(`SELECT COUNT(*) FROM mobile_messages`).Scan(&n); err != nil || n != 0 {
		t.Fatal("clear retained text")
	}
}
func TestBoundPaginationAndTranscriptCut(t *testing.T) {
	r, _, owner, other, _ := fixture(t)
	c := create(t, r, owner)
	another := create(t, r, owner)
	_, page, err := r.List(ctx, owner, true, 1, "")
	if err != nil || page == "" {
		t.Fatal("list cursor")
	}
	if _, _, err = r.List(ctx, other, true, 1, page); !errors.Is(err, securetoken.ErrCursor) {
		t.Fatal(err)
	}
	if _, _, err = r.List(ctx, owner, false, 1, page); !errors.Is(err, securetoken.ErrCursor) {
		t.Fatal(err)
	}
	title := "changed"
	if _, err = r.Update(ctx, owner, c.ID, 1, &title, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err = r.List(ctx, owner, true, 1, page); !errors.Is(err, securetoken.ErrCursor) {
		t.Fatal("catalog mutation did not invalidate cut")
	}
	for i, role := range []string{"user", "assistant"} {
		turn := uuid.NewString()
		if err = r.BindTurn(ctx, owner, c.ID, 1, turn, uint64(i+1)); err != nil {
			t.Fatal(err)
		}
		if _, err = r.Append(ctx, owner, c.ID, 1, turn, uint64(i+1), role, "original"); err != nil {
			t.Fatal(err)
		}
	}
	rows, next, err := r.Messages(ctx, owner, c.ID, 1, 1, "")
	if err != nil || len(rows) != 1 || next == "" {
		t.Fatalf("first page %v %v", rows, err)
	}
	turn := uuid.NewString()
	if err = r.BindTurn(ctx, owner, c.ID, 1, turn, 3); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Append(ctx, owner, c.ID, 1, turn, 3, "user", "later"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = r.Messages(ctx, owner, another.ID, 1, 1, next); !errors.Is(err, securetoken.ErrCursor) {
		t.Fatal("cursor crossed conversation")
	}
	if _, _, err = r.Messages(ctx, owner, c.ID, 1, 1, next+"x"); !errors.Is(err, securetoken.ErrCursor) {
		t.Fatal("tampered cursor")
	}
	rows, next2, err := r.Messages(ctx, owner, c.ID, 1, 1, next)
	if err != nil || len(rows) != 1 || rows[0].Text != "original" || next2 != "" {
		t.Fatalf("cut drifted: %v %v", rows, err)
	}
	if _, err = r.Clear(ctx, owner, c.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, _, err = r.Messages(ctx, owner, c.ID, 2, 1, next); !errors.Is(err, securetoken.ErrCursor) {
		t.Fatal("cursor crossed generation")
	}
}
func TestAppendRequiresExactOwnedTurnBinding(t *testing.T) {
	r, _, owner, other, _ := fixture(t)
	c := create(t, r, owner)
	foreign := create(t, r, other)
	turn := uuid.NewString()
	if err := r.BindTurn(ctx, other, foreign.ID, 1, turn, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Append(ctx, owner, c.ID, 1, turn, 1, "user", "private"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestClearFencesConcurrentTranscriptAppend(t *testing.T) {
	r, path, owner, _, key := fixture(t)
	c := create(t, r, owner)
	turn := uuid.NewString()
	if err := r.BindTurn(ctx, owner, c.ID, 1, turn, 1); err != nil {
		t.Fatal(err)
	}
	other, err := OpenExisting(ctx, path, key)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	start := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		<-start
		_, e := other.Append(ctx, owner, c.ID, 1, turn, 1, "assistant", "late private text")
		done <- e
	}()
	close(start)
	if _, err = r.Clear(ctx, owner, c.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil && !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	rows, _, err := r.Messages(ctx, owner, c.ID, 2, 20, "")
	if err != nil || len(rows) != 0 {
		t.Fatalf("stale append escaped clear: %v %v", rows, err)
	}
}

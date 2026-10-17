package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/httpartifact"
	"github.com/iulita-ai/iulita/internal/httpfetch"
	"go.uber.org/zap"
)

func TestSQLiteHTTPArtifactOwnerQuotaBeyondListBoundAndReopen(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "accounts.sqlite")
	repository, err := New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repository.Close() })
	if err = repository.RunMigrations(ctx); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	limits := httpartifact.Limits{TTL: time.Hour, QuotaGlobal: 10000, QuotaUser: 500}
	artifacts, err := httpartifact.NewStore(root, repository, limits, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	write := func(owner, chat string, size int64) string {
		t.Helper()
		w, writeErr := artifacts.Begin(ctx, httpfetch.ArtifactSpec{OwnerUserID: owner, ChatID: chat, Filename: "synthetic.txt", MaxBytes: size})
		if writeErr != nil {
			t.Fatal(writeErr)
		}
		payload := bytes.Repeat([]byte("x"), int(size))
		if _, writeErr = w.Write(payload); writeErr != nil {
			t.Fatal(writeErr)
		}
		digest := sha256.Sum256(payload)
		if writeErr = w.Commit(hex.EncodeToString(digest[:]), size); writeErr != nil {
			t.Fatal(writeErr)
		}
		identified, ok := w.(interface{ ID() string })
		if !ok {
			t.Fatal("store writer has no artifact identity")
		}
		return identified.ID()
	}
	var first string
	for n := 0; n < 201; n++ {
		id := write("owner", "chat-a", 2)
		if n == 0 {
			first = id
		}
	}
	write("other", "chat-b", 200)
	if rows, listErr := repository.ListHTTPArtifactsByOwner(ctx, "owner"); listErr != nil || len(rows) != 200 {
		t.Fatal("fixture must exceed bounded listing", len(rows), listErr)
	}
	checkSum := func(want int64) {
		t.Helper()
		got, sumErr := repository.SumHTTPArtifactBytesByOwner(ctx, "owner")
		if sumErr != nil || got != want {
			t.Fatal("owner quota truth", got, want, sumErr)
		}
	}
	checkSum(402)
	begin := func(owner string, size int64) (httpfetch.ArtifactWriter, error) {
		return artifacts.Begin(ctx, httpfetch.ArtifactSpec{OwnerUserID: owner, ChatID: "another-chat", Filename: "next.txt", MaxBytes: size})
	}
	if _, err = begin("owner", 99); !errors.Is(err, httpartifact.ErrQuotaExceeded) {
		t.Fatal("bounded listing hid committed owner bytes", err)
	}
	inFlight, err := begin("owner", 98)
	if err != nil {
		t.Fatal("exact available owner capacity", err)
	}
	if _, err = begin("owner", 1); !errors.Is(err, httpartifact.ErrQuotaExceeded) {
		t.Fatal("committed plus in-flight owner capacity", err)
	}
	if err = inFlight.Abort(); err != nil {
		t.Fatal(err)
	}
	// Reopen both the actual SQLite database and file store. The quota must not
	// depend on process-local reservation counters or the bounded history list.
	if err = repository.Close(); err != nil {
		t.Fatal(err)
	}
	repository, err = New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err = httpartifact.NewStore(root, repository, limits, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	checkSum(402)
	if _, err = begin("owner", 99); !errors.Is(err, httpartifact.ErrQuotaExceeded) {
		t.Fatal("owner quota lost after persistent reopen", err)
	}
	if err = artifacts.Delete(ctx, "owner", first); err != nil {
		t.Fatal(err)
	}
	checkSum(400)
	pending, err := begin("owner", 100)
	if err != nil {
		t.Fatal("deletion did not release capacity", err)
	}
	if err = pending.Abort(); err != nil {
		t.Fatal(err)
	}
	foreign, err := begin("other", 300)
	if err != nil {
		t.Fatal("another owner's capacity was charged", err)
	}
	if err = foreign.Abort(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteHTTPArtifactOwnerQuotaIncludesPersistedPartialAndReadyStates(t *testing.T) {
	s := newArtifactTestStore(t)
	ctx := context.Background()
	for _, item := range []struct {
		owner, state string
		size         int64
	}{{"owner", domain.HTTPArtifactReady, 7}, {"owner", domain.HTTPArtifactPartial, 11}, {"owner", domain.HTTPArtifactUnavailable, 101}, {"owner", domain.HTTPArtifactDeleting, 103}, {"other", domain.HTTPArtifactReady, 107}} {
		artifact := &domain.HTTPArtifact{ID: uuid.NewString(), OwnerUserID: item.owner, StorageName: uuid.NewString() + ".bin", Size: item.size, State: item.state, ExpiresAt: time.Now().Add(time.Hour)}
		if err := s.SaveHTTPArtifact(ctx, artifact); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := s.SumHTTPArtifactBytesByOwner(ctx, "owner"); err != nil || got != 18 {
		t.Fatal("ready plus partial aggregate", got, err)
	}
	if got, err := s.SumHTTPArtifactBytesByOwner(ctx, "missing"); err != nil || got != 0 {
		t.Fatal("empty owner aggregate", got, err)
	}
	if got, err := s.SumHTTPArtifactBytes(ctx); err != nil || got != 125 {
		t.Fatal("global aggregate", got, err)
	}
}

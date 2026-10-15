package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
)

func TestQueuedSecurityReservationCancelsBeforeSQLiteBusyTimeout(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "accounts.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err = s.RunMigrations(ctx); err != nil {
		t.Fatal(err)
	}
	const owner = "security-queue-owner"
	if err = s.CreateUser(ctx, &domain.User{ID: owner, Username: owner, PasswordHash: "fixture"}); err != nil {
		t.Fatal(err)
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- s.WithUserSecurity(ctx, owner, func(u *domain.User) error { close(entered); <-release; return nil })
	}()
	<-entered
	defer func() {
		close(release)
		if heldErr := <-done; heldErr != nil {
			t.Error(heldErr)
		}
	}()
	waiting, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	called := false
	err = s.WithUserSecurity(waiting, owner, func(*domain.User) error { called = true; return nil })
	if !errors.Is(err, context.DeadlineExceeded) || called {
		t.Fatal("cancelled reservation entered business section", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("cancelled waiter slept in SQLite busy handler")
	}
}

package google

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
	"go.uber.org/zap"
	"golang.org/x/oauth2"
	gtasks "google.golang.org/api/tasks/v1"
)

func TestAccountPrincipalNeverFallsBackToGlobalCredentials(t *testing.T) {
	t.Setenv("IULITA_GOOGLE_TOKEN", "GLOBAL_SECRET_MUST_NOT_BE_USED")
	store := &mockTokenStore{accounts: []domain.GoogleAccount{{ID: 1, UserID: "owner", IsDefault: true, EncryptedAccessToken: "owned", EncryptedRefreshToken: "refresh", TokenExpiry: time.Now().Add(time.Hour)}}}
	c := NewClient("id", "secret", "", store, nil, zap.NewNop())
	ctx := WithAccountPrincipal(context.Background(), "owner")
	source, err := c.resolveTokenSource(ctx, "owner", "")
	if err != nil {
		t.Fatal(err)
	}
	token, err := source.Token()
	if err != nil || token.AccessToken != "owned" {
		t.Fatal("substituted global credentials", err)
	}
	if _, err = c.resolveTokenSource(ctx, "other", ""); err == nil {
		t.Fatal("principal substitution")
	}
	if _, err = c.resolveTokenSource(ctx, "owner", "foreign-alias"); err == nil {
		t.Fatal("unknown alias fell back")
	}
	store.accounts = nil
	if _, err = c.resolveTokenSource(ctx, "owner", ""); err == nil {
		t.Fatal("missing owned account used global credentials")
	}
	if c.ownedAccountsAvailable(ctx, "owner") {
		t.Fatal("missing account advertised")
	}
}

type nativeRoundTrip func(*http.Request) (*http.Response, error)

func (f nativeRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOwnedGoogleRequestsCancelAndLostMutationDoesNotReplay(t *testing.T) {
	store := &mockTokenStore{accounts: []domain.GoogleAccount{{ID: 1, UserID: "owner", IsDefault: true, EncryptedAccessToken: "owned", TokenExpiry: time.Now().Add(time.Hour)}}}
	c := NewClient("id", "secret", "", store, nil, zap.NewNop())
	started := make(chan struct{})
	var calls atomic.Int32
	api := &http.Client{Transport: nativeRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		close(started)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	root, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := WithAccountPrincipal(context.WithValue(root, oauth2.HTTPClient, api), "owner")
	service, err := c.GetTasksService(ctx, "owner", "")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, callErr := service.Tasklists.List().Do(); done <- callErr }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case callErr := <-done:
		if !errors.Is(callErr, context.Canceled) {
			t.Fatal("cancellation not propagated", callErr)
		}
	case <-time.After(time.Second):
		t.Fatal("account request detached from turn")
	}
	if calls.Load() != 1 {
		t.Fatal("read unexpectedly replayed")
	}
	calls.Store(0)
	api = &http.Client{Transport: nativeRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.Method != "POST" {
			t.Error("unexpected method")
		}
		return nil, errors.New("lost response")
	})}
	ctx = WithAccountPrincipal(context.WithValue(context.Background(), oauth2.HTTPClient, api), "owner")
	service, err = c.GetTasksService(ctx, "owner", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Tasks.Insert("@default", &gtasks.Task{Title: "fixture"}).Do(); err == nil || calls.Load() != 1 {
		t.Fatal("ambiguous mutation replayed", calls.Load(), err)
	}
}

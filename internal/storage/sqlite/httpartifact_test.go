package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
)

func newArtifactTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RunMigrations(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestHTTPStandingGrantsMatrix(t *testing.T) {
	s := newArtifactTestStore(t)
	ctx := context.Background()

	save := func(owner, scope, scheme, host, port string, ttl time.Duration) {
		g := &domain.HTTPStandingGrant{
			OwnerUserID: owner, Scope: scope, Scheme: scheme, Host: host, Port: port,
			ExpiresAt: time.Now().Add(ttl),
		}
		if err := s.SaveHTTPStandingGrant(ctx, g); err != nil {
			t.Fatal(err)
		}
	}

	save("u1", domain.HTTPStandingScopeOrigin, "http", "example.test", "80", time.Hour)
	save("u2", domain.HTTPStandingScopeGlobal, "", "", "", time.Hour)
	save("u3", domain.HTTPStandingScopeOrigin, "http", "old.test", "80", -time.Minute)

	cases := []struct {
		owner, scheme, host, port string
		want                      bool
	}{
		{"u1", "http", "example.test", "80", true},
		{"u1", "http", "example.test", "8080", false},
		{"u1", "https", "example.test", "443", false},
		{"u1", "http", "other.test", "80", false},
		{"u1x", "http", "example.test", "80", false},
		{"u2", "https", "anything.test", "9999", true},
		{"u3", "http", "old.test", "80", false},
		{"u4", "http", "example.test", "80", false},
	}
	for _, tc := range cases {
		got, err := s.HasHTTPStandingGrant(ctx, tc.owner, tc.scheme, tc.host, tc.port)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Fatalf("Has(%s %s://%s:%s) = %v, want %v", tc.owner, tc.scheme, tc.host, tc.port, got, tc.want)
		}
	}

	grants, err := s.ListHTTPStandingGrants(ctx, "u1")
	if err != nil || len(grants) != 1 {
		t.Fatalf("list u1: %v %d", err, len(grants))
	}
	if err := s.RevokeHTTPStandingGrant(ctx, grants[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeHTTPStandingGrant(ctx, grants[0].ID); err != nil {
		t.Fatalf("idempotent revoke: %v", err)
	}
	if ok, _ := s.HasHTTPStandingGrant(ctx, "u1", "http", "example.test", "80"); ok {
		t.Fatal("revoked grant still covers")
	}
}

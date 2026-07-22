package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
)

func TestSlackAccount_Lifecycle(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Missing account returns (nil, nil).
	got, err := store.GetSlackAccountByUserID(ctx, "owner")
	if err != nil {
		t.Fatalf("GetSlackAccountByUserID miss: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for missing account, got %+v", got)
	}

	acc := &domain.SlackAccount{
		UserID:                "owner",
		SlackUserID:           "U1",
		TeamID:                "T1",
		TeamName:              "Acme",
		EncryptedAccessToken:  "enc:utok-a", // gitleaks:allow (fake test fixture)
		EncryptedRefreshToken: "enc:rtok-1", // gitleaks:allow (fake test fixture)
		TokenExpiry:           time.Now().Add(12 * time.Hour).Truncate(time.Second),
		Scopes:                `["search:read"]`,
	}
	if err = store.SaveSlackAccount(ctx, acc); err != nil {
		t.Fatalf("SaveSlackAccount: %v", err)
	}
	if acc.ID == 0 {
		t.Fatal("expected autoincrement ID")
	}

	got, err = store.GetSlackAccountByUserID(ctx, "owner")
	if err != nil {
		t.Fatalf("GetSlackAccountByUserID: %v", err)
	}
	if got == nil || got.SlackUserID != "U1" || got.TeamID != "T1" || got.EncryptedAccessToken != "enc:utok-a" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	// Token rotation update.
	newExpiry := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	if err = store.UpdateSlackTokens(ctx, got.ID, "enc:utok-b", "enc:rtok-2", newExpiry); err != nil {
		t.Fatalf("UpdateSlackTokens: %v", err)
	}
	got, _ = store.GetSlackAccountByUserID(ctx, "owner")
	if got.EncryptedAccessToken != "enc:utok-b" || got.EncryptedRefreshToken != "enc:rtok-2" {
		t.Errorf("tokens not updated: %+v", got)
	}

	// Delete (disconnect).
	if err = store.DeleteSlackAccount(ctx, "owner"); err != nil {
		t.Fatalf("DeleteSlackAccount: %v", err)
	}
	got, _ = store.GetSlackAccountByUserID(ctx, "owner")
	if got != nil {
		t.Fatalf("expected account deleted, got %+v", got)
	}
}

func TestSlackAccount_SingleOwnerUnique(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	first := &domain.SlackAccount{UserID: "owner", SlackUserID: "U1", TeamID: "T1", EncryptedAccessToken: "e1", Scopes: "[]"}
	if err := store.SaveSlackAccount(ctx, first); err != nil {
		t.Fatalf("first save: %v", err)
	}
	// A second row for the same user_id must violate the UNIQUE constraint.
	second := &domain.SlackAccount{UserID: "owner", SlackUserID: "U2", TeamID: "T2", EncryptedAccessToken: "e2", Scopes: "[]"}
	if err := store.SaveSlackAccount(ctx, second); err == nil {
		t.Fatal("expected UNIQUE(user_id) violation on second account for same owner")
	}
}

func TestSlackAccount_GetAny(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	got, err := store.GetAnySlackAccount(ctx)
	if err != nil {
		t.Fatalf("GetAnySlackAccount empty: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil when no account, got %+v", got)
	}

	if err = store.SaveSlackAccount(ctx, &domain.SlackAccount{
		UserID: "owner", SlackUserID: "U1", TeamID: "T1", EncryptedAccessToken: "e1", Scopes: "[]",
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err = store.GetAnySlackAccount(ctx)
	if err != nil {
		t.Fatalf("GetAnySlackAccount: %v", err)
	}
	if got == nil || got.UserID != "owner" {
		t.Fatalf("expected the connected account, got %+v", got)
	}
}

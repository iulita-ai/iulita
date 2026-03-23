package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage"
)

func TestCredential_SaveAndGet(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()

	cred := &domain.Credential{
		Name:      "test.api_key",
		Type:      domain.CredentialTypeAPIKey,
		Scope:     domain.CredentialScopeGlobal,
		Value:     "encrypted-value",
		Encrypted: true,
	}
	if err := store.SaveCredential(ctx, cred); err != nil {
		t.Fatal(err)
	}
	if cred.ID == 0 {
		t.Fatal("expected non-zero ID after save")
	}

	got, err := store.GetCredential(ctx, cred.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "test.api_key" {
		t.Errorf("got name %q, want %q", got.Name, "test.api_key")
	}
	if got.Value != "encrypted-value" {
		t.Errorf("got value %q, want %q", got.Value, "encrypted-value")
	}
}

func TestCredential_GetByName(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()

	cred := &domain.Credential{Name: "claude.api_key", Type: domain.CredentialTypeAPIKey, Value: "sk-123"}
	if err := store.SaveCredential(ctx, cred); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetCredentialByName(ctx, "claude.api_key")
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != "sk-123" {
		t.Errorf("got value %q, want %q", got.Value, "sk-123")
	}

	// Not found case.
	_, err = store.GetCredentialByName(ctx, "nonexistent")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestCredential_GetByNameAndOwner(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()

	cred := &domain.Credential{
		Name:    "google.token",
		Type:    domain.CredentialTypeOAuth2Tokens,
		Scope:   domain.CredentialScopeUser,
		OwnerID: "user-1",
		Value:   "user-token",
	}
	if err := store.SaveCredential(ctx, cred); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetCredentialByNameAndOwner(ctx, "google.token", "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Value != "user-token" {
		t.Errorf("expected user-token, got %v", got)
	}

	// Different owner returns nil.
	got, err = store.GetCredentialByNameAndOwner(ctx, "google.token", "user-2")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("expected nil for different owner, got %v", got)
	}
}

func TestCredential_ListWithFilter(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()

	creds := []*domain.Credential{
		{Name: "a.key", Type: domain.CredentialTypeAPIKey, Scope: domain.CredentialScopeGlobal, Value: "v1"},
		{Name: "b.token", Type: domain.CredentialTypeBotToken, Scope: domain.CredentialScopeGlobal, Value: "v2"},
		{Name: "c.user", Type: domain.CredentialTypeAPIKey, Scope: domain.CredentialScopeUser, OwnerID: "u1", Value: "v3"},
	}
	for _, c := range creds {
		if err := store.SaveCredential(ctx, c); err != nil {
			t.Fatal(err)
		}
	}

	// Filter by scope.
	got, err := store.ListCredentials(ctx, storage.CredentialFilter{Scope: domain.CredentialScopeGlobal})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 global creds, got %d", len(got))
	}

	// Filter by type.
	got, err = store.ListCredentials(ctx, storage.CredentialFilter{Type: domain.CredentialTypeBotToken})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("expected 1 bot_token cred, got %d", len(got))
	}

	// No filter.
	got, err = store.ListCredentials(ctx, storage.CredentialFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("expected 3 creds, got %d", len(got))
	}
}

func TestCredential_Update(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()

	cred := &domain.Credential{Name: "upd.key", Type: domain.CredentialTypeAPIKey, Value: "old"}
	if err := store.SaveCredential(ctx, cred); err != nil {
		t.Fatal(err)
	}

	cred.Value = "new"
	cred.Description = "updated"
	if err := store.UpdateCredential(ctx, cred); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetCredential(ctx, cred.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != "new" {
		t.Errorf("got value %q, want %q", got.Value, "new")
	}
	if got.Description != "updated" {
		t.Errorf("got description %q, want %q", got.Description, "updated")
	}
}

func TestCredential_Delete(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()

	cred := &domain.Credential{Name: "del.key", Type: domain.CredentialTypeAPIKey, Value: "v"}
	if err := store.SaveCredential(ctx, cred); err != nil {
		t.Fatal(err)
	}

	if err := store.DeleteCredential(ctx, cred.ID); err != nil {
		t.Fatal(err)
	}

	_, err := store.GetCredential(ctx, cred.ID)
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestCredentialBinding_SaveAndList(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()

	cred := &domain.Credential{Name: "bind.key", Type: domain.CredentialTypeAPIKey, Value: "v"}
	if err := store.SaveCredential(ctx, cred); err != nil {
		t.Fatal(err)
	}

	b := &domain.CredentialBinding{
		CredentialID: cred.ID,
		ConsumerType: domain.CredentialConsumerChannelInstance,
		ConsumerID:   "tg-main",
		CreatedBy:    "admin",
	}
	if err := store.SaveCredentialBinding(ctx, b); err != nil {
		t.Fatal(err)
	}

	bindings, err := store.ListCredentialBindings(ctx, cred.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 1 {
		t.Fatalf("expected 1 binding, got %d", len(bindings))
	}
	if bindings[0].ConsumerID != "tg-main" {
		t.Errorf("got consumer_id %q, want %q", bindings[0].ConsumerID, "tg-main")
	}
}

func TestCredentialBinding_UniqueConstraint(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()

	cred := &domain.Credential{Name: "uniq.key", Type: domain.CredentialTypeAPIKey, Value: "v"}
	if err := store.SaveCredential(ctx, cred); err != nil {
		t.Fatal(err)
	}

	b := &domain.CredentialBinding{
		CredentialID: cred.ID,
		ConsumerType: domain.CredentialConsumerSkill,
		ConsumerID:   "todoist",
	}
	if err := store.SaveCredentialBinding(ctx, b); err != nil {
		t.Fatal(err)
	}
	// Duplicate should not error (DO NOTHING).
	if err := store.SaveCredentialBinding(ctx, b); err != nil {
		t.Fatal(err)
	}

	bindings, err := store.ListCredentialBindings(ctx, cred.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 1 {
		t.Errorf("expected 1 binding after duplicate insert, got %d", len(bindings))
	}
}

func TestCredentialBinding_ListByConsumer(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()

	c1 := &domain.Credential{Name: "c1.key", Type: domain.CredentialTypeAPIKey, Value: "v1"}
	c2 := &domain.Credential{Name: "c2.key", Type: domain.CredentialTypeAPIKey, Value: "v2"}
	if err := store.SaveCredential(ctx, c1); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCredential(ctx, c2); err != nil {
		t.Fatal(err)
	}

	store.SaveCredentialBinding(ctx, &domain.CredentialBinding{
		CredentialID: c1.ID, ConsumerType: "skill", ConsumerID: "google",
	})
	store.SaveCredentialBinding(ctx, &domain.CredentialBinding{
		CredentialID: c2.ID, ConsumerType: "skill", ConsumerID: "google",
	})

	bindings, err := store.ListCredentialBindingsByConsumer(ctx, "skill", "google")
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 2 {
		t.Errorf("expected 2 bindings for consumer google, got %d", len(bindings))
	}
}

func TestCredentialBinding_Delete(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()

	cred := &domain.Credential{Name: "delbind.key", Type: domain.CredentialTypeAPIKey, Value: "v"}
	if err := store.SaveCredential(ctx, cred); err != nil {
		t.Fatal(err)
	}
	store.SaveCredentialBinding(ctx, &domain.CredentialBinding{
		CredentialID: cred.ID, ConsumerType: "skill", ConsumerID: "todoist",
	})

	if err := store.DeleteCredentialBinding(ctx, cred.ID, "skill", "todoist"); err != nil {
		t.Fatal(err)
	}

	bindings, err := store.ListCredentialBindings(ctx, cred.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 0 {
		t.Errorf("expected 0 bindings after delete, got %d", len(bindings))
	}
}

func TestCredentialAudit_SaveAndList(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()

	cred := &domain.Credential{Name: "aud.key", Type: domain.CredentialTypeAPIKey, Value: "v"}
	if err := store.SaveCredential(ctx, cred); err != nil {
		t.Fatal(err)
	}

	entries := []*domain.CredentialAudit{
		{CredentialID: &cred.ID, CredentialName: "aud.key", Action: "created", Actor: "admin", CreatedAt: time.Now().Add(-2 * time.Second)},
		{CredentialID: &cred.ID, CredentialName: "aud.key", Action: "updated", Actor: "admin", CreatedAt: time.Now().Add(-1 * time.Second)},
		{CredentialID: &cred.ID, CredentialName: "aud.key", Action: "rotated", Actor: "admin", CreatedAt: time.Now()},
	}
	for _, e := range entries {
		if err := store.SaveCredentialAudit(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	got, err := store.ListCredentialAudit(ctx, cred.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 audit entries with limit, got %d", len(got))
	}
	// Should be DESC order: rotated first.
	if got[0].Action != "rotated" {
		t.Errorf("expected first entry to be 'rotated', got %q", got[0].Action)
	}
}

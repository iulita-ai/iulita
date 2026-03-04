package skill_test

import (
	"context"
	"testing"

	"github.com/iulita-ai/iulita/internal/skill"
)

func TestWithUserRole(t *testing.T) {
	ctx := context.Background()

	// Empty by default.
	if got := skill.UserRoleFrom(ctx); got != "" {
		t.Fatalf("expected empty role, got %q", got)
	}

	// Set and retrieve.
	ctx = skill.WithUserRole(ctx, "admin")
	if got := skill.UserRoleFrom(ctx); got != "admin" {
		t.Fatalf("expected admin, got %q", got)
	}

	// Override.
	ctx = skill.WithUserRole(ctx, "user")
	if got := skill.UserRoleFrom(ctx); got != "user" {
		t.Fatalf("expected user, got %q", got)
	}
}

func TestContextRoundtrip(t *testing.T) {
	ctx := context.Background()
	ctx = skill.WithChatID(ctx, "chat-1")
	ctx = skill.WithUserID(ctx, "user-1")
	ctx = skill.WithUserRole(ctx, "admin")

	if got := skill.ChatIDFrom(ctx); got != "chat-1" {
		t.Fatalf("chatID: got %q", got)
	}
	if got := skill.UserIDFrom(ctx); got != "user-1" {
		t.Fatalf("userID: got %q", got)
	}
	if got := skill.UserRoleFrom(ctx); got != "admin" {
		t.Fatalf("role: got %q", got)
	}
}

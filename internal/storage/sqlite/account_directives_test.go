package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/skill/directives"
)

func TestAccountDirectivesSharedAcrossChannelsAndClearDoesNotResurrect(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()
	owner, other := uuid.NewString(), uuid.NewString()
	for i, id := range []string{owner, other} {
		if err := s.CreateUser(ctx, &domain.User{ID: id, Username: []string{"owner", "other"}[i], PasswordHash: "fixture"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SaveDirective(ctx, &domain.Directive{ChatID: "telegram:source", UserID: owner, Content: "historical directive"}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteAccountDirective(ctx, owner, "shared account directive "+strings.Repeat("rule ", 300)+"RULE_AT_END"); err != nil {
		t.Fatal(err)
	}
	for _, read := range []func() (string, error){func() (string, error) { return s.ReadAccountDirective(ctx, owner) }, func() (string, error) { return s.ReadMobileSavedKnowledge(ctx, owner) }} {
		text, err := read()
		if err != nil || !strings.Contains(text, "shared account directive") || strings.Contains(text, "historical directive") || !strings.Contains(text, "RULE_AT_END") {
			t.Fatal("inconsistent account preference", text, err)
		}
	}
	if text, err := s.ReadMobileSavedKnowledge(ctx, other); err != nil || strings.Contains(text, "shared account directive") {
		t.Fatal("foreign directive", err)
	}
	// The existing Telegram skill uses the same owner preference API.
	channel := skill.WithUserID(skill.WithChatID(ctx, "telegram:other-source"), owner)
	if _, err := directives.New(s).Execute(channel, []byte(`{"action":"clear"}`)); err != nil {
		t.Fatal(err)
	}
	if text, err := s.ReadMobileSavedKnowledge(ctx, owner); err != nil || strings.Contains(text, "directive") {
		t.Fatal("cleared old directive resurrected", text, err)
	}
	if err := s.WriteAccountDirective(ctx, other, "other only"); err != nil {
		t.Fatal(err)
	}
	if text, err := s.ReadAccountDirective(ctx, owner); err != nil || text != "" {
		t.Fatal("owner preference overwritten", text, err)
	}
}

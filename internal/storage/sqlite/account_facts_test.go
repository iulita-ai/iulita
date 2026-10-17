package sqlite

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/skill/memory"
)

func TestLegacyForgetDeletesAccountFactWithoutSourceOrOwnerBypass(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()
	owner, other := uuid.NewString(), uuid.NewString()
	for i, id := range []string{owner, other} {
		if err := s.CreateUser(ctx, &domain.User{ID: id, Username: []string{"owner", "other"}[i], PasswordHash: "fixture"}); err != nil {
			t.Fatal(err)
		}
	}
	manual := &domain.Fact{UserID: owner, Content: "mobile manual canary", SourceType: "user"}
	foreign := &domain.Fact{UserID: other, ChatID: "telegram:other", Content: "mobile manual canary", SourceType: "user"}
	for _, fact := range []*domain.Fact{manual, foreign} {
		if err := s.SaveFact(ctx, fact); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO facts(chat_id,user_id,content,source_type) VALUES('MoBiLe:shadow',?,'mobile manual canary','user')`, owner); err != nil {
		t.Fatal(err)
	}
	channel := skill.WithUserID(skill.WithChatID(ctx, "telegram:current"), owner)
	forget := memory.NewForget(s)
	input, _ := json.Marshal(map[string]int64{"id": foreign.ID})
	if _, err := forget.Execute(channel, input); err != nil {
		t.Fatal(err)
	}
	if facts, err := s.SearchFactsByUser(ctx, other, "canary", 10); err != nil || len(facts) != 1 {
		t.Fatal("foreign fact deleted", err)
	}
	input, _ = json.Marshal(map[string]int64{"id": manual.ID})
	if out, err := forget.Execute(channel, input); err != nil || !strings.Contains(out, "deleted") {
		t.Fatal("Telegram could not delete native fact", out, err)
	}
	if facts, err := s.SearchFactsByUser(ctx, owner, "canary", 10); err != nil || len(facts) != 0 {
		t.Fatal("native fact remains", err)
	}
	if _, err := forget.Execute(channel, []byte(`{"query":"canary"}`)); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM facts WHERE chat_id='MoBiLe:shadow'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("legacy exclusion removed", count, err)
	}
}

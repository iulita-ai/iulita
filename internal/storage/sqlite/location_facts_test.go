package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
)

func TestLocationFactsChronologyAndNamespaceBeforeLimit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().Add(-time.Minute)
	for _, f := range []*domain.Fact{
		{ChatID: "chat", UserID: "owner", SourceType: "user", Content: "User lives in London", CreatedAt: now.Add(-time.Hour), LastAccessedAt: now, AccessCount: 100},
		{ChatID: "chat", UserID: "owner", SourceType: "user", Content: "Пользователь живёт в Берлине", CreatedAt: now},
		{ChatID: "chat", UserID: "other", SourceType: "user", Content: "User lives in Paris", CreatedAt: now.Add(time.Second)},
		{ChatID: "chat", UserID: "owner", SourceType: "document", Content: "User lives in Paris", CreatedAt: now.Add(time.Second)},
		{ChatID: "mobile:conversation:secret", UserID: "owner", SourceType: "user", Content: "User lives in Tokyo", CreatedAt: now.Add(time.Second)},
	} {
		// Direct insert models future private mobile rows as the namespace tests do.
		if _, err := s.db.NewInsert().Model(f).Exec(ctx); err != nil {
			t.Fatal(err)
		}
	}
	facts, err := s.GetLocationFactsByUser(ctx, "owner", 1)
	if err != nil || len(facts) != 1 || facts[0].Content != "Пользователь живёт в Берлине" {
		t.Fatalf("%+v %v", facts, err)
	}
	facts, err = s.GetLocationFactsByUser(ctx, "", 1)
	if err != nil || len(facts) != 0 {
		t.Fatalf("empty owner: %+v %v", facts, err)
	}
}

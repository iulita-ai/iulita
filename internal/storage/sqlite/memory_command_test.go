package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
)

func TestAccountFactCountExcludesOtherOwnersAndMobileNamespace(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for _, f := range []*domain.Fact{
		{UserID: "owner", Content: "account"},
		{UserID: "owner", ChatID: "tgconv:topic", Content: "topic"},
		{UserID: "other", Content: "other"},
		{UserID: "owner", ChatID: "mobile:private", Content: "mobile"},
		{UserID: "owner", ChatID: "MOBILE:malformed", Content: "reserved"},
	} {
		if _, err := s.db.NewInsert().Model(f).Exec(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.CountFactsByUser(ctx, "owner"); err != nil || n != 2 {
		t.Fatalf("%d %v", n, err)
	}
	if n, err := s.CountFactsByUser(ctx, ""); err == nil || n != 0 {
		t.Fatalf("empty owner became global count: %d %v", n, err)
	}
	for _, i := range []*domain.Insight{{UserID: "owner", Content: "account"}, {UserID: "owner", ChatID: "mobile:private", Content: "mobile"}, {UserID: "owner", ChatID: "MOBILE:malformed", Content: "reserved"}, {UserID: "other", Content: "other"}} {
		if _, err := s.db.NewInsert().Model(i).Exec(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.CountInsightsByUser(ctx, "owner"); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
}

func TestLocationCandidatesIncludeRetractionsBeforeLimit(t *testing.T) {
	for _, statement := range []string{"I am no longer in Tokyo", "User is no longer in Tokyo", "Я уже не в Москве", "Я сейчас не в Москве", "I am in Paris"} {
		t.Run(statement, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			now := time.Now().Add(-time.Minute)
			for _, f := range []*domain.Fact{{UserID: "owner", Content: "I live in Berlin", CreatedAt: now.Add(-time.Hour)}, {UserID: "owner", Content: statement, CreatedAt: now}} {
				if err := s.SaveFact(ctx, f); err != nil {
					t.Fatal(err)
				}
			}
			facts, err := s.GetLocationFactsByUser(ctx, "owner", 1)
			if err != nil || len(facts) != 1 || facts[0].Content != statement {
				t.Fatalf("new retraction filtered out: %+v %v", facts, err)
			}
		})
	}
}

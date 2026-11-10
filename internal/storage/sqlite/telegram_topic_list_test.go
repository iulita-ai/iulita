package sqlite

import (
	"context"
	"fmt"
	"testing"

	"github.com/iulita-ai/iulita/internal/domain"
)

func TestTopicListIsOwnerInstanceChatScoped(t *testing.T) {
	s, e := New(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	ctx := context.Background()
	if e = s.RunMigrations(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.EnsureTelegramTopics(ctx); e != nil {
		t.Fatal(e)
	}
	for i := 1; i <= 25; i++ {
		r := &domain.TelegramConversationRoute{ConversationID: fmt.Sprint("topic", i), InstanceID: "a", OwnerID: "alice", ChatID: 42, ThreadID: i, Private: true}
		if i == 21 {
			r.InstanceID = "b"
		}
		if i == 22 {
			r.OwnerID = "bob"
		}
		if i == 23 {
			r.ChatID = 43
		}
		if i == 24 {
			r.Private = false
		}
		if e = s.PutTelegramRoute(ctx, r); e != nil {
			t.Fatal(e)
		}
		state := &domain.TelegramTopicState{ConversationID: r.ConversationID, Name: fmt.Sprint("Name", i), Status: "open"}
		if i == 25 {
			state.Status = "deleted"
		}
		if i == 1 {
			state.Status = "paused"
		}
		if e = s.PutTelegramTopicState(ctx, state); e != nil {
			t.Fatal(e)
		}
	}
	rows, e := s.ListTelegramTopics(ctx, "a", "alice", 42, 0, 21)
	if e != nil || len(rows) != 20 {
		t.Fatal(len(rows), e)
	}
	for _, r := range rows {
		if r.ThreadID > 20 {
			t.Fatalf("foreign route leaked %+v", r)
		}
	}
	last, e := s.ListTelegramTopics(ctx, "a", "alice", 42, 19, 21)
	if e != nil || len(last) != 1 || last[0].Status != "paused" {
		t.Fatal(last, e)
	}
}

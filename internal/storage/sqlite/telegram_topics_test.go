package sqlite

import (
	"context"
	"testing"

	"github.com/iulita-ai/iulita/internal/domain"
)

func TestTopicCreateUnknownBlocksBlindLogicalRetry(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	op := &domain.TelegramTopicOperation{ID: "one", Digest: "same-create", OwnerID: "alice", InstanceID: "a", ChatID: 42, Action: "new", Name: "Research"}
	if _, claimed, e := s.ClaimTelegramTopicOperation(ctx, op); e != nil || !claimed {
		t.Fatal(claimed, e)
	}
	op.Status = "unknown_outcome"
	if e := s.SaveTelegramTopicOperation(ctx, op); e != nil {
		t.Fatal(e)
	}
	next := *op
	next.ID = "two"
	previous, claimed, e := s.ClaimTelegramTopicOperation(ctx, &next)
	if e != nil || claimed || previous.ID != "one" {
		t.Fatal(previous, claimed, e)
	}
	op.Status = "created"
	op.ThreadID = 77
	if e = s.SaveTelegramTopicOperation(ctx, op); e != nil {
		t.Fatal(e)
	}
	previous, claimed, e = s.ClaimTelegramTopicOperation(ctx, op)
	if e != nil || claimed || previous.ThreadID != 77 {
		t.Fatal(previous, claimed, e)
	}
}

func TestLocalTopicPauseCommitsStateAndResultTogether(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if e := s.PutTelegramRoute(ctx, &domain.TelegramConversationRoute{ConversationID: "topic", InstanceID: "a", ChatID: 42, ThreadID: 77, OwnerID: "alice", Private: true}); e != nil {
		t.Fatal(e)
	}
	state := &domain.TelegramTopicState{ConversationID: "topic", Name: "Research", Status: "open"}
	if e := s.PutTelegramTopicState(ctx, state); e != nil {
		t.Fatal(e)
	}
	op := &domain.TelegramTopicOperation{ID: "pause", Digest: "pause", OwnerID: "alice", InstanceID: "a", ChatID: 42, ThreadID: 77, Action: "pause"}
	state.Status = "paused"
	if e := s.ApplyLocalTelegramTopicOperation(ctx, op, state); e == nil {
		t.Fatal("unclaimed operation changed state")
	}
	current, e := s.GetTelegramTopicState(ctx, "topic")
	if e != nil || current.Status != "open" {
		t.Fatal("failed local op did not roll back", current, e)
	}
	if _, claimed, checkErr := s.ClaimTelegramTopicOperation(ctx, op); checkErr != nil || !claimed {
		t.Fatal(claimed, checkErr)
	}
	if e = s.ApplyLocalTelegramTopicOperation(ctx, op, state); e != nil {
		t.Fatal(e)
	}
	current, e = s.GetTelegramTopicState(ctx, "topic")
	if e != nil || current.Status != "paused" {
		t.Fatal(current, e)
	}
	saved, e := s.GetTelegramTopicOperation(ctx, "pause", "alice", "a")
	if e != nil || saved.Status != "succeeded" {
		t.Fatal("local success marked uncertain", saved, e)
	}
}

func TestLocalTopicTransitionRejectsForeignRouteAndDeletedTopic(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for _, route := range []domain.TelegramConversationRoute{{ConversationID: "alice-topic", InstanceID: "a", ChatID: 42, ThreadID: 77, OwnerID: "alice", Private: true}, {ConversationID: "bob-topic", InstanceID: "a", ChatID: 43, ThreadID: 78, OwnerID: "bob", Private: true}} {
		r := route
		if e := s.PutTelegramRoute(ctx, &r); e != nil {
			t.Fatal(e)
		}
	}
	for _, test := range []struct{ id, key, owner, status, action string }{{"foreign", "bob-topic", "alice", "paused", "pause"}, {"wrongowner", "alice-topic", "bob", "paused", "pause"}, {"badtransition", "alice-topic", "alice", "open", "pause"}, {"deleted", "alice-topic", "alice", "open", "resume"}} {
		if test.id == "deleted" {
			if e := s.PutTelegramTopicState(ctx, &domain.TelegramTopicState{ConversationID: test.key, Status: "deleted"}); e != nil {
				t.Fatal(e)
			}
		}
		op := &domain.TelegramTopicOperation{ID: test.id, Digest: test.id, OwnerID: test.owner, InstanceID: "a", ChatID: 42, ThreadID: 77, Action: test.action}
		if _, ok, e := s.ClaimTelegramTopicOperation(ctx, op); e != nil || !ok {
			t.Fatal(ok, e)
		}
		if e := s.ApplyLocalTelegramTopicOperation(ctx, op, &domain.TelegramTopicState{ConversationID: test.key, Status: test.status}); e == nil {
			t.Fatalf("invalid local state mutation accepted: %+v", test)
		}
	}
}

func TestLocalTopicOperationCannotRetargetSameOwnerIntent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for i, key := range []string{"first", "second"} {
		if e := s.PutTelegramRoute(ctx, &domain.TelegramConversationRoute{ConversationID: key, InstanceID: "a", ChatID: 42, ThreadID: 77 + i, OwnerID: "alice", Private: true}); e != nil {
			t.Fatal(e)
		}
	}
	op := &domain.TelegramTopicOperation{ID: "pin", Digest: "approved-digest", OwnerID: "alice", InstanceID: "a", ChatID: 42, ThreadID: 77, Action: "pause"}
	if _, ok, e := s.ClaimTelegramTopicOperation(ctx, op); e != nil || !ok {
		t.Fatal(ok, e)
	}
	forged := *op
	forged.ThreadID = 78
	if e := s.ApplyLocalTelegramTopicOperation(ctx, &forged, &domain.TelegramTopicState{ConversationID: "second", Status: "paused"}); e == nil {
		t.Fatal("approved operation retargeted another own topic")
	}
	state, e := s.GetTelegramTopicState(ctx, "second")
	if e != nil || state != nil {
		t.Fatal("failed intent did not roll back", state, e)
	}
	forged = *op
	forged.Action = "resume"
	if e := s.ApplyLocalTelegramTopicOperation(ctx, &forged, &domain.TelegramTopicState{ConversationID: "first", Status: "open"}); e == nil {
		t.Fatal("approved operation action changed")
	}
}

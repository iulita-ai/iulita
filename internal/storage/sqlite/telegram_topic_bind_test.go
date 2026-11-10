package sqlite

import (
	"context"
	"testing"

	"github.com/iulita-ai/iulita/internal/domain"
)

func TestLegacyTopicBindingRequiresExactApprovedOneWayTransition(t *testing.T) {
	s, scope := deletionStore(t)
	ctx := context.Background()
	scope.ConversationID = "tgconv:legacy-bind"
	scope.ThreadID = 101
	r := &domain.TelegramConversationRoute{ConversationID: scope.ConversationID, InstanceID: scope.InstanceID, OwnerID: scope.OwnerID, ChatID: scope.ChatID, ThreadID: scope.ThreadID, Private: true}
	if e := s.PutTelegramRoute(ctx, r); e != nil {
		t.Fatal(e)
	}
	scope.OperationID = "bind-op"
	scope.Digest = "digest-includes-target-bot"
	if e := s.BindLegacyTelegramTopic(ctx, scope); e == nil {
		t.Fatal("unapproved binding allowed")
	}
	op := &domain.TelegramTopicOperation{BotID: scope.BotID, ID: scope.OperationID, Digest: scope.Digest, OwnerID: scope.OwnerID, InstanceID: scope.InstanceID, ChatID: scope.ChatID, ThreadID: scope.ThreadID, Action: "bind"}
	if _, claimed, e := s.ClaimTelegramTopicOperation(ctx, op); e != nil || !claimed {
		t.Fatal(claimed, e)
	}
	for _, mutate := range []func(*domain.TelegramTopicDeletionScope){func(v *domain.TelegramTopicDeletionScope) { v.OwnerID = "mallory" }, func(v *domain.TelegramTopicDeletionScope) { v.ThreadID++ }, func(v *domain.TelegramTopicDeletionScope) { v.Digest = "wrong" }, func(v *domain.TelegramTopicDeletionScope) { v.BotID = 0 }, func(v *domain.TelegramTopicDeletionScope) { v.BotID++ }} {
		bad := scope
		mutate(&bad)
		if e := s.BindLegacyTelegramTopic(ctx, bad); e == nil {
			t.Fatal("invalid binding allowed", bad)
		}
	}
	if e := s.BindLegacyTelegramTopic(ctx, scope); e != nil {
		t.Fatal(e)
	}
	if e := s.BindLegacyTelegramTopic(ctx, scope); e != nil {
		t.Fatal("exact success not idempotent", e)
	}
	got, e := s.GetTelegramRoute(ctx, scope.ConversationID)
	if e != nil || got.BotID != scope.BotID {
		t.Fatal(got, e)
	}
	saved, e := s.GetTelegramTopicOperation(ctx, scope.OperationID, scope.OwnerID, scope.InstanceID)
	if e != nil || saved.Status != "succeeded" {
		t.Fatal(saved, e)
	}
	bad := scope
	bad.BotID++
	if e = s.BindLegacyTelegramTopic(ctx, bad); e == nil {
		t.Fatal("bound identity replaced")
	}
}
func TestLegacyTopicBindingCannotResurrectDeletedState(t *testing.T) {
	s, scope := deletionStore(t)
	ctx := context.Background()
	scope.ConversationID = "tgconv:legacy-deleted"
	scope.OperationID = "op"
	scope.Digest = "digest"
	if e := s.PutTelegramRoute(ctx, &domain.TelegramConversationRoute{ConversationID: scope.ConversationID, InstanceID: scope.InstanceID, OwnerID: scope.OwnerID, ChatID: scope.ChatID, ThreadID: scope.ThreadID, Private: true}); e != nil {
		t.Fatal(e)
	}
	if _, _, e := s.ClaimTelegramTopicOperation(ctx, &domain.TelegramTopicOperation{BotID: scope.BotID, ID: scope.OperationID, Digest: scope.Digest, OwnerID: scope.OwnerID, InstanceID: scope.InstanceID, ChatID: scope.ChatID, ThreadID: scope.ThreadID, Action: "bind"}); e != nil {
		t.Fatal(e)
	}
	if e := s.PutTelegramTopicState(ctx, &domain.TelegramTopicState{ConversationID: scope.ConversationID, Status: "deleted"}); e != nil {
		t.Fatal(e)
	}
	if e := s.BindLegacyTelegramTopic(ctx, scope); e == nil {
		t.Fatal("binding resurrected deleted state")
	}
}

func TestTopicListBotScopeIncludesLegacyButExcludesPreviousBot(t *testing.T) {
	s, scope := deletionStore(t)
	ctx := context.Background()
	for _, r := range []domain.TelegramConversationRoute{{ConversationID: "tgconv:legacy", InstanceID: scope.InstanceID, OwnerID: scope.OwnerID, ChatID: scope.ChatID, ThreadID: 101, Private: true}, {BotID: 999, ConversationID: "tgconv:replacement", InstanceID: scope.InstanceID, OwnerID: scope.OwnerID, ChatID: scope.ChatID, ThreadID: 102, Private: true}} {
		if e := s.PutTelegramRoute(ctx, &r); e != nil {
			t.Fatal(e)
		}
	}
	rows, e := s.ListTelegramTopics(ctx, scope.InstanceID, scope.OwnerID, scope.ChatID, 0, 21, 999)
	if e != nil || len(rows) != 2 {
		t.Fatal(rows, e)
	}
	for _, r := range rows {
		if r.BotID != 0 && r.BotID != 999 {
			t.Fatal("previous bot data disclosed", r)
		}
	}
}

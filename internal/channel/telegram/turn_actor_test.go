package telegram

import (
	"context"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/skill/interact"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
)

func TestTurnFactoryIgnoresMutableLastSenderAndRejectsSubstitution(t *testing.T) {
	s, e := sqlite.New(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	ctx := context.Background()
	if e = s.RunMigrations(ctx); e != nil {
		t.Fatal(e)
	}
	route := &domain.TelegramConversationRoute{ConversationID: "42", InstanceID: "bot-a", ChatID: 42, OwnerID: "alice", Private: true}
	if e = s.PutTelegramRoute(ctx, route); e != nil {
		t.Fatal(e)
	}
	c := &Channel{store: s, instanceID: "bot-a", confirmSenders: map[string]int64{"42": 999}}
	actor := interact.Actor{UserID: "alice", Channel: "telegram", ChannelInstance: "bot-a", ExternalID: "42"}
	p := c.ConfirmerForActor(ctx, "42", actor)
	if p == nil || p.(*telegramConfirmer).fromTg != 42 {
		t.Fatal("mutable sender replaced turn actor")
	}
	for _, bad := range []interact.Actor{{UserID: "bob", Channel: "telegram", ChannelInstance: "bot-a", ExternalID: "42"}, {UserID: "alice", Channel: "telegram", ChannelInstance: "bot-b", ExternalID: "42"}, {UserID: "alice", Channel: "telegram", ChannelInstance: "bot-a", ExternalID: "999"}} {
		if c.ConfirmerForActor(ctx, "42", bad) != nil || c.PrompterForActor(ctx, "42", bad) != nil {
			t.Fatal("actor substitution accepted")
		}
	}
}
func TestGroupDebounceNeverCombinesDifferentActors(t *testing.T) {
	messages := make(chan channel.IncomingMessage, 2)
	d := newDebouncer(time.Hour, func(msg channel.IncomingMessage) { messages <- msg })
	d.add(channel.IncomingMessage{ChannelType: "telegram", ChatID: "-100", UserID: "alice", Text: "A"})
	d.add(channel.IncomingMessage{ChannelType: "telegram", ChatID: "-100", UserID: "bob", Text: "B"})
	d.flushAll()
	if len(messages) != 2 {
		t.Fatal("shared messages combined across principals")
	}
}

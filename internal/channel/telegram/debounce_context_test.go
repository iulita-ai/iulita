package telegram

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/assistant"
	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/skill"
	"go.uber.org/zap"
)

func TestContextCommandQueuesWithoutBlockingCallbacks(t *testing.T) {
	started, approval := make(chan struct{}), make(chan struct{})
	events := make(chan string, 6)
	d := newDebouncer(0, func(m channel.IncomingMessage) {
		if m.Text == "first" {
			close(started)
			<-approval
		}
		events <- m.Text
	})
	d.add(channel.IncomingMessage{ChatID: "topic1", Text: "first"})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first not started")
	}
	d.add(channel.IncomingMessage{ChatID: "topic1", Text: "second"})
	d.control("topic1", true, func() { events <- "clear" })
	d.add(channel.IncomingMessage{ChatID: "topic1", Text: "after clear"})
	// A different conversation and the approval dispatcher remain usable.
	d.add(channel.IncomingMessage{ChatID: "topic2", Text: "other topic"})
	select {
	case e := <-events:
		if e != "other topic" {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("other topic blocked")
	}
	close(approval)
	d.flushAll()
	for _, want := range []string{"first", "second", "clear", "after clear"} {
		select {
		case got := <-events:
			if got != want {
				t.Fatalf("got %s want %s", got, want)
			}
		case <-time.After(time.Second):
			t.Fatal("missing queued work")
		}
	}
}

func TestClearDiscardsPendingBurstCompactFlushesIt(t *testing.T) {
	for _, clear := range []bool{true, false} {
		events := make(chan string, 3)
		d := newDebouncer(time.Hour, func(m channel.IncomingMessage) { events <- m.Text })
		d.onDiscard = func(channel.IncomingMessage) { events <- "discarded" }
		d.add(channel.IncomingMessage{ChatID: "topic", Text: "before command"})
		d.control("topic", clear, func() { events <- "command" })
		d.flushAll()
		if !clear {
			if got := <-events; got != "before command" {
				t.Fatal(got)
			}
		} else if got := <-events; got != "discarded" {
			t.Fatal("clear did not finish discarded seed receipt")
		}
		if got := <-events; got != "command" {
			t.Fatal(got)
		}
		select {
		case extra := <-events:
			t.Fatalf("pre-clear context resurrected: %s", extra)
		default:
		}
	}
}

func TestShutdownDrainsTurnEnqueuedByTopicPreparation(t *testing.T) {
	events := make(chan string, 1)
	d := newDebouncer(time.Hour, func(m channel.IncomingMessage) { events <- m.Text })
	d.control("prepare:42", false, func() { d.add(channel.IncomingMessage{ChatID: "newtopic", Text: "seed"}) })
	d.flushAll()
	select {
	case got := <-events:
		if got != "seed" {
			t.Fatal(got)
		}
	default:
		t.Fatal("accepted seed lost during shutdown")
	}
}

func TestMergedTopicTurnRetainsPrivateProofAndReceipts(t *testing.T) {
	first := channel.IncomingMessage{ChannelType: "telegram", ChatID: "topic", UserID: "42", ResolvedUserID: "alice", ChannelInstanceID: "a", ProvenPrivateRoute: true, MessageID: 301, Text: "one"}
	second := first
	second.MessageID = 302
	second.Text = "two"
	merged := mergeMessages([]channel.IncomingMessage{first, second})
	if merged.ChannelType != "telegram" || !merged.ProvenPrivateRoute || len(merged.RelatedMessageIDs) != 2 || merged.RelatedMessageIDs[1] != 302 {
		t.Fatal("private identity or seed receipts lost")
	}
	second.UserID = "43"
	if mergeMessages([]channel.IncomingMessage{first, second}).ProvenPrivateRoute {
		t.Fatal("foreign actor inherited private proof")
	}
	first.ProvenPrivateRoute = false
	second.ProvenPrivateRoute = false
	group := mergeMessages([]channel.IncomingMessage{first, second})
	if group.ChannelType != "telegram" || group.ProvenPrivateRoute {
		t.Fatal("group lost Telegram privacy boundary")
	}
	// The real assistant must reject before touching its absent private store
	// or provider even after a burst was merged by the transport.
	a := assistant.New(nil, nil, skill.NewRegistry(), "", "", 200000, zap.NewNop())
	response, err := a.HandleMessage(context.Background(), group)
	if err != nil || !strings.Contains(response, "private chat") {
		t.Fatalf("merged group reached private assistant: %q %v", response, err)
	}
}

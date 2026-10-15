package eventbus

import (
	"context"
	"testing"

	"go.uber.org/zap"
)

func TestPrivatePayloadsNeverReachGlobalSubscribers(t *testing.T) {
	b := New(zap.NewNop())
	received := 0
	b.Subscribe(MessageReceived, func(context.Context, Event) error { received++; return nil })
	b.Subscribe(ResponseSent, func(context.Context, Event) error { received++; return nil })
	for _, id := range []string{"mobile:conversation:x", "MOBILE:invalid"} {
		for _, evt := range []Event{
			{MessageReceived, MessageReceivedPayload{ChatID: id, Text: "private"}},
			{MessageReceived, &MessageReceivedPayload{ChatID: id, Text: "private"}},
			{ResponseSent, ResponseSentPayload{ChatID: id, Response: "private"}},
			{ResponseSent, &ResponseSentPayload{ChatID: id, Response: "private"}},
			{FactSaved, FactSavedPayload{ChatID: id}},
		} {
			if GlobalEventAllowed(evt) {
				t.Fatal("private event allowed")
			}
			b.Publish(context.Background(), evt)
		}
	}
	for _, p := range []any{map[string]any{"ChatID": "mobile:x", "Text": "private"}, "private", (*MessageReceivedPayload)(nil)} {
		b.Publish(context.Background(), Event{MessageReceived, p})
	}
	if received != 0 {
		t.Fatal("private content reached subscriber")
	}
	for _, id := range []string{"web:owner", "123"} {
		b.Publish(context.Background(), Event{MessageReceived, MessageReceivedPayload{ChatID: id}})
	}
	if received != 2 {
		t.Fatalf("ordinary events lost: %d", received)
	}
}

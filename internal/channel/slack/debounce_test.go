package slack

import (
	"sync"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/channel"
)

func TestDebouncer_ImmediateWithZeroWindow(t *testing.T) {
	var mu sync.Mutex
	var received []channel.IncomingMessage

	d := newDebouncer(0, func(msg channel.IncomingMessage) {
		mu.Lock()
		received = append(received, msg)
		mu.Unlock()
	})

	d.add(channel.IncomingMessage{ChatID: "chat1", Text: "hello"})
	d.add(channel.IncomingMessage{ChatID: "chat2", Text: "world"})

	// Give goroutines time to complete.
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(received))
	}
}

func TestDebouncer_MergesRapidMessages(t *testing.T) {
	var mu sync.Mutex
	var received []channel.IncomingMessage

	d := newDebouncer(100*time.Millisecond, func(msg channel.IncomingMessage) {
		mu.Lock()
		received = append(received, msg)
		mu.Unlock()
	})

	d.add(channel.IncomingMessage{ChatID: "chat1", Text: "hello"})
	d.add(channel.IncomingMessage{ChatID: "chat1", Text: "world"})

	// Wait for debounce to fire.
	time.Sleep(250 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Fatalf("expected 1 merged message, got %d", len(received))
	}
	if received[0].Text != "hello\nworld" {
		t.Errorf("expected merged text 'hello\\nworld', got %q", received[0].Text)
	}
}

func TestDebouncer_FlushAll(t *testing.T) {
	var mu sync.Mutex
	var received []channel.IncomingMessage

	d := newDebouncer(10*time.Second, func(msg channel.IncomingMessage) {
		mu.Lock()
		received = append(received, msg)
		mu.Unlock()
	})

	d.add(channel.IncomingMessage{ChatID: "chat1", Text: "pending"})
	d.flushAll()

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Fatalf("flushAll: expected 1 message, got %d", len(received))
	}
}

func TestMergeMessages_Single(t *testing.T) {
	msgs := []channel.IncomingMessage{
		{ChatID: "c1", Text: "hello", UserID: "u1"},
	}
	merged := mergeMessages(msgs)
	if merged.Text != "hello" {
		t.Errorf("expected 'hello', got %q", merged.Text)
	}
}

func TestMergeMessages_Multiple(t *testing.T) {
	msgs := []channel.IncomingMessage{
		{ChatID: "c1", Text: "hello", UserID: "u1", UserName: "Alice"},
		{ChatID: "c1", Text: "world", UserID: "u1"},
		{ChatID: "c1", Text: "", UserID: "u1"}, // empty text skipped
	}
	merged := mergeMessages(msgs)
	if merged.Text != "hello\nworld" {
		t.Errorf("expected 'hello\\nworld', got %q", merged.Text)
	}
	if merged.UserName != "Alice" {
		t.Errorf("expected first message's UserName 'Alice', got %q", merged.UserName)
	}
}

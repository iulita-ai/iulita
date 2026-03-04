package interact

import (
	"context"
	"testing"
)

func TestContextRoundTrip(t *testing.T) {
	ctx := context.Background()

	// Without a prompter, should return NoopAsker.
	asker := PrompterFrom(ctx)
	if _, ok := asker.(NoopAsker); !ok {
		t.Fatal("expected NoopAsker when no prompter in context")
	}
	_, err := asker.Ask(ctx, "test", nil)
	if err != ErrNoPrompter {
		t.Fatalf("expected ErrNoPrompter, got %v", err)
	}

	// With a prompter, should return it.
	mock := &mockAsker{answer: "hello"}
	ctx = WithPrompter(ctx, mock)
	got := PrompterFrom(ctx)
	ans, err := got.Ask(ctx, "question", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ans != "hello" {
		t.Errorf("expected 'hello', got %q", ans)
	}
}

func TestNoopAsker(t *testing.T) {
	asker := NoopAsker{}
	_, err := asker.Ask(context.Background(), "test", []Option{{ID: "a", Label: "A"}})
	if err != ErrNoPrompter {
		t.Fatalf("expected ErrNoPrompter, got %v", err)
	}
}

func TestCompositeFactory(t *testing.T) {
	mock := &mockAsker{answer: "found"}
	f1 := &mockFactory{prefix: "tg:", asker: nil}   // doesn't match
	f2 := &mockFactory{prefix: "web:", asker: mock} // matches
	f3 := &mockFactory{prefix: "con:", asker: nil}  // not reached

	composite := NewCompositeFactory(f1, f2, f3)
	got := composite.PrompterFor("web:user1")
	if got == nil {
		t.Fatal("expected non-nil prompter")
	}
	ans, err := got.Ask(context.Background(), "q", nil)
	if err != nil {
		t.Fatal(err)
	}
	if ans != "found" {
		t.Errorf("expected 'found', got %q", ans)
	}

	// No match.
	got = composite.PrompterFor("unknown:123")
	if got != nil {
		t.Error("expected nil when no factory matches")
	}
}

type mockAsker struct {
	answer string
}

func (m *mockAsker) Ask(_ context.Context, _ string, _ []Option) (string, error) {
	return m.answer, nil
}

type mockFactory struct {
	prefix string
	asker  PromptAsker
}

func (f *mockFactory) PrompterFor(chatID string) PromptAsker {
	if len(chatID) >= len(f.prefix) && chatID[:len(f.prefix)] == f.prefix {
		return f.asker
	}
	return nil
}

package skill

import (
	"context"
	"testing"
	"time"
)

func TestWithDeadlineExtenderAndFrom(t *testing.T) {
	ctx := context.Background()

	// Not set → nil.
	if fn := DeadlineExtenderFrom(ctx); fn != nil {
		t.Fatal("expected nil when not set")
	}

	// Set → retrievable.
	ctx = WithDeadlineExtender(ctx, DefaultDeadlineExtender)
	fn := DeadlineExtenderFrom(ctx)
	if fn == nil {
		t.Fatal("expected non-nil after injection")
	}
}

func TestDefaultDeadlineExtender_ReplacesDeadline(t *testing.T) {
	// Parent context with a very short deadline.
	parent, parentCancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer parentCancel()

	// Extend to 5 seconds — should NOT inherit the 10ms deadline.
	extended, extCancel := DefaultDeadlineExtender(parent, 5*time.Second)
	defer extCancel()

	// Wait longer than the parent deadline.
	time.Sleep(50 * time.Millisecond)

	// Parent should be done.
	if parent.Err() == nil {
		t.Error("parent should have expired")
	}

	// Extended should still be alive.
	if extended.Err() != nil {
		t.Errorf("extended should not have expired, got: %v", extended.Err())
	}

	// Extended should have a deadline ~5s from creation.
	deadline, ok := extended.Deadline()
	if !ok {
		t.Fatal("extended should have a deadline")
	}
	remaining := time.Until(deadline)
	if remaining < 3*time.Second || remaining > 6*time.Second {
		t.Errorf("remaining = %v, want ~5s", remaining)
	}
}

func TestDefaultDeadlineExtender_PreservesValues(t *testing.T) {
	ctx := context.Background()
	ctx = WithChatID(ctx, "test-chat")
	ctx = WithUserID(ctx, "test-user")

	extended, extCancel := DefaultDeadlineExtender(ctx, time.Minute)
	defer extCancel()

	// Context values should be preserved.
	if ChatIDFrom(extended) != "test-chat" {
		t.Errorf("ChatID lost: %q", ChatIDFrom(extended))
	}
	if UserIDFrom(extended) != "test-user" {
		t.Errorf("UserID lost: %q", UserIDFrom(extended))
	}
}

func TestDefaultDeadlineExtender_CancelWorks(t *testing.T) {
	extended, extCancel := DefaultDeadlineExtender(context.Background(), time.Hour)
	extCancel()

	if extended.Err() == nil {
		t.Error("extended should be cancelled after extCancel()")
	}
}

func TestDeadlineExtender_ParentCancelDoesNotAffectExtended(t *testing.T) {
	parent, parentCancel := context.WithCancel(context.Background())

	extended, extCancel := DefaultDeadlineExtender(parent, 5*time.Second)
	defer extCancel()

	// Cancel parent.
	parentCancel()

	// Extended should NOT be affected (WithoutCancel breaks the chain).
	if extended.Err() != nil {
		t.Errorf("extended should not be cancelled when parent is, got: %v", extended.Err())
	}
}

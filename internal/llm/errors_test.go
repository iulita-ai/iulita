package llm

import (
	"errors"
	"fmt"
	"testing"
)

func TestIsContextTooLarge(t *testing.T) {
	// Direct.
	if !IsContextTooLarge(ErrContextTooLarge) {
		t.Error("direct ErrContextTooLarge should match")
	}

	// Wrapped.
	wrapped := fmt.Errorf("claude completion: %w", ErrContextTooLarge)
	if !IsContextTooLarge(wrapped) {
		t.Error("wrapped ErrContextTooLarge should match")
	}

	// Double wrapped.
	double := fmt.Errorf("outer: %w", wrapped)
	if !IsContextTooLarge(double) {
		t.Error("double-wrapped should match")
	}

	// Unrelated error.
	other := errors.New("some other error")
	if IsContextTooLarge(other) {
		t.Error("unrelated error should not match")
	}

	// Nil.
	if IsContextTooLarge(nil) {
		t.Error("nil should not match")
	}
}

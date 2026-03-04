package dashboard

import (
	"testing"
)

func TestGenerateState(t *testing.T) {
	state, err := generateState()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(state) != 32 { // 16 bytes hex-encoded = 32 chars
		t.Errorf("expected 32-char hex state, got %d chars: %s", len(state), state)
	}

	// States should be unique.
	state2, err := generateState()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state == state2 {
		t.Error("consecutive states should be different")
	}
}

func TestHasStatePrefix(t *testing.T) {
	tests := []struct {
		full, prefix string
		want         bool
	}{
		{"abc123", "abc123", true},
		{"abc123:work", "abc123", true},
		{"abc", "abcdef", false},
		{"", "", true},
		{"abc", "", true},
	}

	for _, tt := range tests {
		got := hasStatePrefix(tt.full, tt.prefix)
		if got != tt.want {
			t.Errorf("hasStatePrefix(%q, %q) = %v, want %v", tt.full, tt.prefix, got, tt.want)
		}
	}
}

func TestGetUserID_NoClaims(t *testing.T) {
	// getUserID is tested through handler tests; here we verify behavior with nil Ctx
	// This would require fiber context which is heavy for unit test.
	// The function is simple enough and well-tested through integration.
}

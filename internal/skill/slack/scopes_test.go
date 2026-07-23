package slack

import (
	"strings"
	"testing"
)

func TestHasWriteScope(t *testing.T) {
	tests := []struct {
		granted string
		want    bool
	}{
		{"search:read,channels:history,channels:read,groups:history,groups:read,users:read", false},
		{"search:read", false},
		{"search:read,chat:write", true},
		{"files:write", true},
		{"reactions:write", true},
		{"", false},
	}
	for _, tt := range tests {
		if got := HasWriteScope(tt.granted); got != tt.want {
			t.Errorf("HasWriteScope(%q) = %v, want %v", tt.granted, got, tt.want)
		}
	}
}

// TestRequiredUserScopes_NoWriteScope is the read-only invariant at the scope
// layer: the scopes we ever REQUEST must contain no write scope.
func TestRequiredUserScopes_NoWriteScope(t *testing.T) {
	for _, sc := range RequiredUserScopes() {
		if strings.Contains(sc, "write") {
			t.Errorf("requested scope %q is a write scope; the connection must be read-only", sc)
		}
	}
	if HasWriteScope(strings.Join(RequiredUserScopes(), ",")) {
		t.Error("RequiredUserScopes must not contain any write scope")
	}
}

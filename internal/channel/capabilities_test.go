package channel

import (
	"context"
	"testing"
)

func TestChannelCaps_Has(t *testing.T) {
	tests := []struct {
		name  string
		caps  ChannelCaps
		query ChannelCaps
		want  bool
	}{
		{"streaming present", CapStreaming | CapMarkdown, CapStreaming, true},
		{"markdown present", CapStreaming | CapMarkdown, CapMarkdown, true},
		{"both present", CapStreaming | CapMarkdown, CapStreaming | CapMarkdown, true},
		{"buttons missing", CapStreaming | CapMarkdown, CapButtons, false},
		{"partial missing", CapStreaming, CapStreaming | CapButtons, false},
		{"zero caps", 0, CapStreaming, false},
		{"query zero", CapStreaming, 0, true}, // zero query = vacuously true
		{"all caps", CapStreaming | CapMarkdown | CapReactions | CapButtons | CapTyping | CapHTML, CapHTML, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.caps.Has(tt.query); got != tt.want {
				t.Errorf("ChannelCaps(%d).Has(%d) = %v, want %v", tt.caps, tt.query, got, tt.want)
			}
		})
	}
}

func TestCapsContext(t *testing.T) {
	ctx := context.Background()

	// No caps set — should return zero value.
	if caps := CapsFrom(ctx); caps != 0 {
		t.Errorf("expected 0 caps from empty context, got %d", caps)
	}

	// Set and retrieve.
	caps := CapStreaming | CapMarkdown | CapButtons
	ctx = WithCaps(ctx, caps)
	got := CapsFrom(ctx)
	if got != caps {
		t.Errorf("CapsFrom = %d, want %d", got, caps)
	}
	if !got.Has(CapStreaming) {
		t.Error("expected CapStreaming")
	}
	if !got.Has(CapButtons) {
		t.Error("expected CapButtons")
	}
	if got.Has(CapHTML) {
		t.Error("unexpected CapHTML")
	}
}

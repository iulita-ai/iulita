package telegram

import (
	"strings"
	"testing"
	"time"
)

func TestBookmarkKeyboard(t *testing.T) {
	t.Run("short response gets both buttons", func(t *testing.T) {
		kb := bookmarkKeyboard("💾 Save", "📋 Copy", "remember:abc", "full answer")
		if len(kb.InlineKeyboard) != 1 || len(kb.InlineKeyboard[0]) != 2 {
			t.Fatalf("want one row with two buttons, got %+v", kb.InlineKeyboard)
		}
		row := kb.InlineKeyboard[0]
		if row[0].Text != "💾 Save" || row[0].CallbackData != "remember:abc" {
			t.Errorf("remember button wrong: %+v", row[0])
		}
		if row[1].Text != "📋 Copy" || row[1].CopyText == nil || row[1].CopyText.Text != "full answer" {
			t.Errorf("copy button wrong: %+v", row[1])
		}
	})

	t.Run("long response omits copy button (256-char API limit)", func(t *testing.T) {
		long := strings.Repeat("x", copyTextMaxLen+1)
		kb := bookmarkKeyboard("💾 Save", "📋 Copy", "remember:abc", long)
		if len(kb.InlineKeyboard) != 1 || len(kb.InlineKeyboard[0]) != 1 {
			t.Fatalf("want one row with one button, got %+v", kb.InlineKeyboard)
		}
		if kb.InlineKeyboard[0][0].CopyText != nil {
			t.Error("copy button should be omitted for long text")
		}
	})

	t.Run("exactly 256 runes keeps copy button", func(t *testing.T) {
		exact := strings.Repeat("у", copyTextMaxLen)
		kb := bookmarkKeyboard("💾 Save", "📋 Copy", "remember:abc", exact)
		if len(kb.InlineKeyboard[0]) != 2 || kb.InlineKeyboard[0][1].CopyText == nil || kb.InlineKeyboard[0][1].CopyText.Text != exact {
			t.Fatalf("copy button should be present at the limit: %+v", kb.InlineKeyboard)
		}
	})
}

func TestSentTracker(t *testing.T) {
	tr := newSentTracker()

	tr.record(1, 100, "first")
	tr.record(1, 101, "second")

	if text, ok := tr.text(1, 100); !ok || text != "first" {
		t.Errorf("text(1,100) = %q, %v", text, ok)
	}
	if _, ok := tr.text(2, 100); ok {
		t.Error("text on unknown chat should miss")
	}

	// Per-chat cap: only the newest sentTrackPerChat survive.
	for i := 0; i < sentTrackPerChat+10; i++ {
		tr.record(1, 200+i, "bulk")
	}
	if _, ok := tr.text(1, 100); ok {
		t.Error("capped message should be evicted")
	}
	if _, ok := tr.text(1, 259); !ok {
		t.Error("newest bulk message should survive")
	}
	if _, ok := tr.text(1, 209); ok {
		t.Error("bulk message outside the cap window should be evicted")
	}

	// Deletable prunes by TTL and caps at 100.
	ids := tr.deletable(1)
	if len(ids) != sentTrackPerChat {
		t.Errorf("deletable = %d ids, want %d", len(ids), sentTrackPerChat)
	}
	for _, id := range ids {
		if id < 200 || id > 200+sentTrackPerChat+9 {
			t.Errorf("unexpected id %d", id)
		}
	}

	// Old entries are pruned by TTL on deletable.
	tr.mu.Lock()
	list := tr.msgs[9]
	tr.msgs[9] = append(list, sentMsg{msgID: 900, text: "old", at: time.Now().Add(-49 * time.Hour)})
	tr.mu.Unlock()
	if ids := tr.deletable(9); len(ids) != 0 {
		t.Errorf("expired message should not be deletable, got %v", ids)
	}
}

func TestSpoilerAndExpandableQuote(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "spoiler span",
			in:   "the answer is ||42|| maybe",
			want: "the answer is <tg-spoiler>42</tg-spoiler> maybe",
		},
		{
			name: "single pipes are not spoilers",
			in:   "a | b | c",
			want: "a | b | c",
		},
		{
			name: "short quote stays regular",
			in:   "> one line",
			want: "<blockquote>one line</blockquote>",
		},
		{
			name: "long quote becomes expandable",
			in:   "> l1\n> l2\n> l3\n> l4\n> l5",
			want: "<blockquote expandable>l1\nl2\nl3\nl4\nl5</blockquote>",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toTelegramHTML(tt.in); got != tt.want {
				t.Errorf("toTelegramHTML() = %q, want %q", got, tt.want)
			}
		})
	}
}

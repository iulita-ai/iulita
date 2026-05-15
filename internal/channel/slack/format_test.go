package slack

import (
	"testing"
	"unicode/utf8"
)

func TestToMrkdwn(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "bold conversion",
			input:    "This is **bold** text",
			expected: "This is *bold* text",
		},
		{
			name:     "strikethrough conversion",
			input:    "This is ~~deleted~~ text",
			expected: "This is ~deleted~ text",
		},
		{
			name:     "link conversion",
			input:    "Visit [Google](https://google.com) now",
			expected: "Visit <https://google.com|Google> now",
		},
		{
			name:     "heading conversion",
			input:    "# Main Title\n\nSome text",
			expected: "*Main Title*\n\nSome text",
		},
		{
			name:     "h2 heading",
			input:    "## Subtitle",
			expected: "*Subtitle*",
		},
		{
			name:     "inline code preserved",
			input:    "Use `fmt.Println` here",
			expected: "Use `fmt.Println` here",
		},
		{
			name:     "code block language stripped",
			input:    "```go\nfmt.Println(\"hello\")\n```",
			expected: "```\nfmt.Println(\"hello\")\n```",
		},
		{
			name:     "code block without language",
			input:    "```\nsome code\n```",
			expected: "```\nsome code\n```",
		},
		{
			name:     "multiple conversions",
			input:    "**Bold** and [link](http://ex.com) and ~~strike~~",
			expected: "*Bold* and <http://ex.com|link> and ~strike~",
		},
		{
			name:     "plain text unchanged",
			input:    "Just plain text",
			expected: "Just plain text",
		},
		{
			name:     "bold inside code block not converted",
			input:    "```\n**not bold**\n```",
			expected: "```\n**not bold**\n```",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "blockquote preserved",
			input:    "> This is a quote",
			expected: "> This is a quote",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToMrkdwn(tt.input)
			if got != tt.expected {
				t.Errorf("ToMrkdwn(%q):\n  got  = %q\n  want = %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestSplitMessage(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		maxLen   int
		expected int // number of chunks
	}{
		{"short text", "hello", 10, 1},
		{"exact length", "hello", 5, 1},
		{"needs splitting", "line1\nline2\nline3", 10, 3},
		{"empty text", "", 10, 0},
		{"very long no newline", "abcdefghij", 5, 2},
		// Each Cyrillic letter is 2 bytes. A naive byte-split at maxLen=5
		// would land mid-rune; truncateRunes must back off to a rune boundary.
		{"multibyte no newline", "АБВГДЕЖ", 5, 4},
		// Emoji are 4 bytes each.
		{"emoji boundary", "😀😀😀", 5, 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks := splitMessage(tt.text, tt.maxLen)
			if len(chunks) != tt.expected {
				t.Errorf("splitMessage(%q, %d): got %d chunks, want %d", tt.text, tt.maxLen, len(chunks), tt.expected)
			}
			// Verify no chunk exceeds maxLen and every chunk is valid UTF-8.
			for i, chunk := range chunks {
				if len(chunk) > tt.maxLen {
					t.Errorf("chunk[%d] length %d exceeds maxLen %d", i, len(chunk), tt.maxLen)
				}
				if !utf8.ValidString(chunk) {
					t.Errorf("chunk[%d] is not valid UTF-8: %q", i, chunk)
				}
			}
		})
	}
}

func TestSplitMessage_InvalidUTF8ProgressGuard(t *testing.T) {
	// All bytes are UTF-8 continuation bytes; no rune starts exist.
	// splitMessage must still terminate (force-advance one byte).
	bad := "\x80\x80\x80\x80\x80"
	chunks := splitMessage(bad, 3)
	total := 0
	for _, c := range chunks {
		total += len(c)
	}
	if total != len(bad) {
		t.Errorf("expected total chunks length %d, got %d", len(bad), total)
	}
}

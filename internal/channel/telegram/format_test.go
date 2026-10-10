package telegram

import (
	"strings"
	"testing"
)

func TestToTelegramHTML(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "plain text unchanged",
			in:   "hello world",
			want: "hello world",
		},
		{
			name: "double asterisk bold",
			in:   "**bold** text",
			want: "<b>bold</b> text",
		},
		{
			name: "underscore bold",
			in:   "__bold__ text",
			want: "<b>bold</b> text",
		},
		{
			name: "nested bold italic",
			in:   "***both*** and **bold with _italic_ inside**",
			want: "<i><b>both</b></i> and <b>bold with <i>italic</i> inside</b>",
		},
		{
			name: "headings become bold blocks",
			in:   "### Header\n\n#### Level 4\n\nbody",
			want: "<b>Header</b>\n\n<b>Level 4</b>\n\nbody",
		},
		{
			name: "strikethrough preserved",
			in:   "~~removed~~ stays",
			want: "<s>removed</s> stays",
		},
		{
			name: "html special characters escaped",
			in:   "AT&T, 5 < 10 and 7 > 3, \"quotes\"",
			want: "AT&amp;T, 5 &lt; 10 and 7 &gt; 3, &quot;quotes&quot;",
		},
		{
			name: "inline code escaped",
			in:   "run `go test **not bold**` now",
			want: "run <code>go test **not bold**</code> now",
		},
		{
			name: "fenced code block escaped, language ignored",
			in:   "before\n\n```go\nif x < y && y > z {\n\t**not bold**\n}\n```\n\nafter",
			want: "before\n\n<pre>if x &lt; y &amp;&amp; y &gt; z {\n\t**not bold**\n}\n</pre>\n\nafter",
		},
		{
			name: "links",
			in:   "see [docs](https://example.com/a?b=1) here",
			want: `see <a href="https://example.com/a?b=1">docs</a> here`,
		},
		{
			name: "autolinks",
			in:   "visit https://example.com now",
			want: `visit <a href="https://example.com">https://example.com</a> now`,
		},
		{
			name: "blockquote",
			in:   "> quoted text",
			want: "<blockquote>quoted text</blockquote>",
		},
		{
			name: "bullet list",
			in:   "- one\n- two\n- three",
			want: "• one\n• two\n• three",
		},
		{
			name: "ordered list",
			in:   "1. first\n2. second",
			want: "1. first\n2. second",
		},
		{
			name: "ordered list custom start",
			in:   "3. third\n4. fourth",
			want: "3. third\n4. fourth",
		},
		{
			name: "nested lists indented",
			in:   "- top\n  - nested\n- top2",
			want: "• top\n  • nested\n• top2",
		},
		{
			name: "raw html stripped",
			in:   "<script>alert(1)</script>\n\nvisible",
			want: "visible",
		},
		{
			name: "thematic break",
			in:   "above\n\n---\n\nbelow",
			want: "above\n\n───────\n\nbelow",
		},
		{
			name: "table rendered as aligned monospace rows",
			in:   "| Name | Value |\n|------|-------|\n| a    | 1     |",
			want: "<pre>Name | Value\n-----+------\na    | 1</pre>",
		},
		{
			name: "table columns aligned to widest cell",
			in:   "| Когда | Погода |\n|---|---|\n| Утро | Ясно |\n| Вечер | Дождь с грозой |",
			want: "<pre>Когда | Погода\n------+---------------\nУтро  | Ясно\nВечер | Дождь с грозой</pre>",
		},
		{
			name: "emphasis inside table cell suppressed",
			in:   "| h **b** |\n|---|\n| c |",
			want: "<pre>h b\n---\nc</pre>",
		},
		{
			name: "weather-style response",
			in:   "**🌍 Погода в Ницце — 8 октября**\n\nСейчас **17°C**, ощущается как 16°C.\n\n- Утром солнечно\n- Вечером облачно",
			want: "<b>🌍 Погода в Ницце — 8 октября</b>\n\nСейчас <b>17°C</b>, ощущается как 16°C.\n\n• Утром солнечно\n• Вечером облачно",
		},
		{
			name: "empty string",
			in:   "",
			want: "",
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

func TestTelegramHTMLChunks(t *testing.T) {
	t.Run("short text is a single chunk", func(t *testing.T) {
		got := telegramHTMLChunks("**hi** there")
		if len(got) != 1 || got[0] != "<b>hi</b> there" {
			t.Errorf("got %q, want single chunk %q", got, "<b>hi</b> there")
		}
	})

	t.Run("tags are never split across chunks", func(t *testing.T) {
		var para strings.Builder
		para.WriteString("<b>start</b>\n\n")
		for i := 0; i < 200; i++ {
			para.WriteString(strings.Repeat("x", 40))
			if i != 199 {
				para.WriteString("\n\n")
			}
		}
		// Build markdown instead: paragraphs of x's.
		var md strings.Builder
		md.WriteString("**start**\n\n")
		for i := 0; i < 200; i++ {
			md.WriteString(strings.Repeat("x", 40))
			if i != 199 {
				md.WriteString("\n\n")
			}
		}
		got := telegramHTMLChunks(md.String())
		if len(got) < 2 {
			t.Fatalf("expected multiple chunks, got %d", len(got))
		}
		for i, ch := range got {
			if len(ch) > maxMessageLen {
				t.Errorf("chunk %d exceeds maxLen: %d", i, len(ch))
			}
			if strings.Count(ch, "<b>") != strings.Count(ch, "</b>") {
				t.Errorf("chunk %d has unbalanced <b> tags: %q", i, ch[:60])
			}
		}
	})

	t.Run("oversized code block rewraps pre per chunk", func(t *testing.T) {
		var md strings.Builder
		md.WriteString("```\n")
		for i := 0; i < 150; i++ {
			md.WriteString(strings.Repeat("y", 60) + "\n")
		}
		md.WriteString("```")
		got := telegramHTMLChunks(md.String())
		if len(got) < 2 {
			t.Fatalf("expected multiple chunks, got %d", len(got))
		}
		for i, ch := range got {
			if len(ch) > maxMessageLen {
				t.Errorf("chunk %d exceeds maxLen: %d", i, len(ch))
			}
			if !strings.HasPrefix(ch, "<pre>") || !strings.HasSuffix(ch, "</pre>") {
				t.Errorf("chunk %d not wrapped in <pre>: %q...", i, ch[:min(40, len(ch))])
			}
		}
	})

	t.Run("empty input returns nil", func(t *testing.T) {
		if got := telegramHTMLChunks(""); got != nil {
			t.Errorf("got %q, want nil", got)
		}
	})
}

func TestHardSplitHTMLPlain(t *testing.T) {
	// A non-pre oversized block splits on word boundaries.
	long := strings.Repeat("word ", 2000)
	got := hardSplitHTML(long, 1000)
	for i, ch := range got {
		if len(ch) > 1000 {
			t.Errorf("chunk %d exceeds maxLen: %d", i, len(ch))
		}
	}
	if len(got) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(got))
	}
}

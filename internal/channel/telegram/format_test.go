package telegram

import "testing"

func TestToTelegramMarkdown(t *testing.T) {
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
			name: "double asterisk bold converts to single",
			in:   "**bold** text",
			want: "*bold* text",
		},
		{
			name: "underscore bold converts",
			in:   "__bold__ text",
			want: "*bold* text",
		},
		{
			name: "headings convert to bold",
			in:   "### Header\n#### Level 4\nbody",
			want: "*Header*\n*Level 4*\nbody",
		},
		{
			name: "strikethrough markers stripped",
			in:   "~~removed~~ stays",
			want: "removed stays",
		},
		{
			name: "bullets convert to dots",
			in:   "- one\n- two\n* three",
			want: "• one\n• two\n• three",
		},
		{
			name: "links preserved",
			in:   "see [docs](https://example.com) here",
			want: "see [docs](https://example.com) here",
		},
		{
			name: "inline code preserved",
			in:   "run `go test **not bold**` now",
			want: "run `go test **not bold**` now",
		},
		{
			name: "code block content untouched, language hint stripped",
			in:   "before\n```go\n**not bold**\n# not heading\n```\nafter",
			want: "before\n```\n**not bold**\n# not heading\n```\nafter",
		},
		{
			name: "bold inside heading keeps inner bold without nesting",
			in:   "## **Important** notice",
			want: "*Important* notice",
		},
		{
			name: "bold spanning words and lines",
			in:   "**multi word bold** and\nnext *single* line",
			want: "*multi word bold* and\nnext *single* line",
		},
		{
			name: "empty string",
			in:   "",
			want: "",
		},
		{
			name: "weather-style response",
			in:   "**🌍 Погода в Ницце — 8 октября**\n\nСейчас **17°C**, ощущается как 16°C.\n\n- Утром солнечно\n- Вечером облачно",
			want: "*🌍 Погода в Ницце — 8 октября*\n\nСейчас *17°C*, ощущается как 16°C.\n\n• Утром солнечно\n• Вечером облачно",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toTelegramMarkdown(tt.in); got != tt.want {
				t.Errorf("toTelegramMarkdown() = %q, want %q", got, tt.want)
			}
		})
	}
}

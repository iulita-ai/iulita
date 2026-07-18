package importer

import (
	"strings"
	"testing"
)

func TestReconstructMessage(t *testing.T) {
	tests := []struct {
		name          string
		msg           dumpMessage
		wantContains  []string
		wantExcludes  []string
		wantEmpty     bool
		wantSkippedBn int
	}{
		{
			name:         "plain text",
			msg:          dumpMessage{Text: "hello world"},
			wantContains: []string{"hello world"},
		},
		{
			name: "empty text falls back to text blocks only",
			msg: dumpMessage{
				Text: "",
				Content: []dumpContentBlock{
					{Type: "thinking", Text: "SECRET REASONING"},
					{Type: "text", Text: "visible answer"},
					{Type: "tool_use", Text: "call_api(secret_key)"},
					{Type: "tool_result", Text: "API SECRET RESPONSE"},
					{Type: "token_budget", Text: "1234"},
				},
			},
			wantContains: []string{"visible answer"},
			wantExcludes: []string{"SECRET REASONING", "call_api", "API SECRET RESPONSE", "1234"},
		},
		{
			name: "attachment extracted content inlined",
			msg: dumpMessage{
				Text:        "see attached",
				Attachments: []dumpAttachment{{FileName: "notes.txt", FileType: "txt", ExtractedContent: "line1\nline2"}},
			},
			wantContains: []string{"see attached", "attachment: notes.txt", "(txt)", "line1\nline2"},
		},
		{
			name: "binary files counted and marked when message has text",
			msg: dumpMessage{
				Text:  "look at this photo",
				Files: []dumpFile{{FileUUID: "u1", FileName: "photo"}, {FileUUID: "u2", FileName: "diagram.png"}},
			},
			wantContains:  []string{"look at this photo", "[binary file skipped: photo]", "[binary file skipped: diagram.png]"},
			wantSkippedBn: 2,
		},
		{
			name: "fully empty message with only a binary skips content but counts binary",
			msg: dumpMessage{
				Text:  "",
				Files: []dumpFile{{FileUUID: "u1", FileName: "photo"}},
			},
			wantEmpty:     true,
			wantSkippedBn: 1,
		},
		{
			name:      "empty text and no blocks or attachments is empty",
			msg:       dumpMessage{Text: "   "},
			wantEmpty: true,
		},
		{
			name: "attachment with empty extracted content is ignored",
			msg: dumpMessage{
				Text:        "",
				Attachments: []dumpAttachment{{FileName: "empty.txt", ExtractedContent: "  "}},
			},
			wantEmpty: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, skipped := reconstructMessage(&tt.msg)
			if skipped != tt.wantSkippedBn {
				t.Errorf("skippedBinaries = %d, want %d", skipped, tt.wantSkippedBn)
			}
			if tt.wantEmpty {
				if content != "" {
					t.Errorf("expected empty content, got %q", content)
				}
				return
			}
			if content == "" {
				t.Fatal("expected non-empty content")
			}
			for _, want := range tt.wantContains {
				if !strings.Contains(content, want) {
					t.Errorf("content missing %q\ngot: %q", want, content)
				}
			}
			for _, ex := range tt.wantExcludes {
				if strings.Contains(content, ex) {
					t.Errorf("content must not contain %q\ngot: %q", ex, content)
				}
			}
		})
	}
}

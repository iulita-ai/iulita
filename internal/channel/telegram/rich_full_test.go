package telegram

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"
)

func TestRichReaderKeepsTablesMediaCreditsButtonsWithoutAuthority(t *testing.T) {
	fixture := `{"blocks":[{"type":"table","cells":[[{"text":"Name","is_header":true},{"text":"Value"}],[{"text":"Alpha"},{"text":{"type":"bold","text":"42"}}]],"caption":"Table caption"},{"type":"photo","photo":[],"caption":{"text":"Picture label","credit":"Photo credit"}},{"type":"buttons","buttons":[{"text":"Visible button","callback_data":"secret_tool_execute","url":"https://hidden.invalid"}]},{"type":"details","summary":"Summary","blocks":[{"type":"list","items":[{"label":"1.","has_checkbox":true,"is_checked":true,"blocks":[{"type":"paragraph","text":"Checked item"}]}]}]}]}`
	var rich models.RichMessage
	if e := json.Unmarshal([]byte(fixture), &rich); e != nil {
		t.Fatal(e)
	}
	got := richPlain(&rich)
	for _, want := range []string{"Name | Value", "Alpha | 42", "Table caption", "Picture label — Photo credit", "Visible button", "Summary", "[x] Checked item"} {
		if !strings.Contains(got, want) {
			t.Fatalf("lost display semantics %q in %q", want, got)
		}
	}
	if strings.Contains(got, "hidden.invalid") || strings.Contains(got, "secret_tool_execute") {
		t.Fatal("hidden authority leaked")
	}
}
func TestAllRichTextVariantsHaveDisplayPolicy(t *testing.T) {
	variants := []string{"bold", "italic", "underline", "strikethrough", "spoiler", "date_time", "text_mention", "subscript", "superscript", "marked", "code", "url", "email_address", "phone_number", "mention", "hashtag", "cashtag", "bot_command", "anchor_link", "reference", "reference_link"}
	for _, variant := range variants {
		t.Run(variant, func(t *testing.T) {
			var text models.RichText
			e := json.Unmarshal([]byte(`{"type":"`+variant+`","text":"shown","url":"https://hidden.invalid","name":"hidden"}`), &text)
			if e != nil {
				t.Fatal(e)
			}
			if got := richPlain(text); got != "shown" {
				t.Fatalf("lost %s display %q", variant, got)
			}
		})
	}
	var redacted models.RichText
	_ = json.Unmarshal([]byte(`{"type":"ton_address","text":"raw-wallet-address","address":"raw-wallet-address"}`), &redacted)
	if richPlain(redacted) != "[redacted]" {
		t.Fatal("wallet not redacted")
	}
}
func TestOutboundRichRendererKeepsStructuredMarkdown(t *testing.T) {
	input := "# Plan\n\n**Bold** and [safe](https://example.org)\n\n| Item | Value |\n| --- | --- |\n| A | 42 |\n\n1. first\n2. second\n\n> quoted\n\n```go\nfmt.Println(42)\n```\n"
	rich, e := renderRichMessage(input)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := json.Marshal(rich)
	if e != nil {
		t.Fatal(e)
	}
	var decoded models.RichMessage
	if e = json.Unmarshal(raw, &decoded); e != nil {
		t.Fatal(e)
	}
	got := richPlain(decoded)
	for _, want := range []string{"Plan", "Bold and safe", "Item | Value", "A | 42", "first", "second", "quoted", "fmt.Println(42)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("renderer lost %q: %s", want, got)
		}
	}
	for _, typ := range []string{`"heading"`, `"table"`, `"list"`, `"blockquote"`, `"pre"`} {
		if !strings.Contains(string(raw), typ) {
			t.Fatalf("not structured: %s", raw)
		}
	}
	evil, e := renderRichMessage("[click](javascript:execute) **ok**")
	if e != nil {
		t.Fatal(e)
	}
	payload, _ := json.Marshal(evil)
	if strings.Contains(string(payload), "javascript") {
		t.Fatal("active untrusted URL")
	}
}

func TestRichRendererFallsBackInsteadOfTruncatingManyBlocks(t *testing.T) {
	input := strings.Repeat("paragraph\n\n", 150)
	if _, e := renderRichMessage(input); e == nil {
		t.Fatal("renderer silently dropped blocks instead of allowing complete chunked fallback")
	}
}

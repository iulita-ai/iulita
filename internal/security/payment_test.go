package security

import (
	"strings"
	"testing"
)

func TestPaymentRedaction(t *testing.T) {
	for _, text := range []string{"card 4111 1111 1111 1111", "card 4111111111111111", "EQ" + strings.Repeat("a", 46), "0:" + strings.Repeat("a", 64)} {
		got := RedactPaymentData(text)
		if got == text {
			t.Fatalf("not redacted: %q", text)
		}
		if strings.Contains(got, "4111111111111111") || strings.Contains(got, strings.Repeat("a", 46)) {
			t.Fatal("full payment value survived")
		}
	}
	for _, text := range []string{"2026-11-10", "order 1234567890123", "+79991234567", "invoice 11111111111111111111"} {
		if got := RedactPaymentData(text); got != text {
			t.Fatalf("false positive: %q -> %q", text, got)
		}
	}
}
func TestPaymentEntityUTF16(t *testing.T) {
	text := "😀 card 1234567890123 end"
	got := RedactPaymentEntities(text, []TextEntity{{Type: "bank_card_number", Offset: 8, Length: 13}})
	if got != "😀 card •••• 0123 end" {
		t.Fatalf("got %q", got)
	}
	for _, e := range []TextEntity{{Type: "bank_card_number", Offset: 1, Length: 1}, {Type: "bank_card_number", Offset: -1, Length: 5}, {Type: "bank_card_number", Offset: 2, Length: 999999999}} {
		if got := RedactPaymentEntities("😀 abc", []TextEntity{e}); got != "😀 abc" {
			t.Fatalf("invalid range changed text: %q", got)
		}
	}
}
func TestSensitiveTextMasksCredential(t *testing.T) {
	secret := "sk-proj-" + strings.Repeat("a", 30)
	if got := RedactSensitiveText("key " + secret); strings.Contains(got, secret) {
		t.Fatal("secret survived")
	}
}

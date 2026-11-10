package assistant

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/llm"
)

func TestRequestRedactsOldContextAndToolResults(t *testing.T) {
	card := "4111111111111111"
	secret := "sk-proj-" + strings.Repeat("a", 30)
	req := llm.Request{Message: card, SystemPrompt: secret, History: []domain.ChatMessage{{Content: card}}, ToolExchanges: []llm.ToolExchange{{AssistantText: card, ReasoningContent: secret, Results: []llm.ToolResult{{Content: secret + card}}}}}
	redactRequest(&req)
	for _, s := range []string{req.Message, req.SystemPrompt, req.History[0].Content, req.ToolExchanges[0].AssistantText, req.ToolExchanges[0].ReasoningContent, req.ToolExchanges[0].Results[0].Content} {
		if strings.Contains(s, card) || strings.Contains(s, secret) {
			t.Fatal("sensitive data survived model boundary")
		}
	}
}

func TestRequestMasksTextDocumentsAndPreservesExistingOpaqueAttachmentPath(t *testing.T) {
	req := llm.Request{Documents: []llm.DocumentAttachment{{MimeType: "text/plain", Data: []byte("4111111111111111")}, {MimeType: "application/octet-stream", Data: []byte("opaque attachment")}, {MimeType: "text/plain", Data: []byte{0xff, 0xfe, 0x00}}}}
	redactRequest(&req)
	if strings.Contains(string(req.Documents[0].Data), "4111111111111111") || string(req.Documents[1].Data) != "opaque attachment" || !bytes.Equal(req.Documents[2].Data, []byte{0xff, 0xfe, 0x00}) {
		t.Fatal("text masking or legacy attachment preservation failed")
	}
}

func TestToolInputsAreRedactedWithoutBreakingJSON(t *testing.T) {
	raw := json.RawMessage(`{"id":42,"card":4111111111111111,"token":"abc123456789","nested":["4111 1111 1111 1111"]}`) // gitleaks:allow -- Synthetic redaction fixture, never a real credential.
	got := redactToolInput(raw)
	if !json.Valid(got) || strings.Contains(string(got), "4111111111111111") || strings.Contains(string(got), "abc123456789") || strings.Contains(string(got), "4111 1111") {
		t.Fatalf("unsafe tool replay %s", got)
	}
	if !strings.Contains(string(got), `"id":42`) {
		t.Fatal("ordinary identifier changed")
	}
}

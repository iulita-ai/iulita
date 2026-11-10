package assistant

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/security"
)

// redactRequest is the last defense for old history and tool content, including
// private data read from stores created before ingress masking was introduced.
func redactRequest(req *llm.Request) {
	req.Message = security.RedactSensitiveText(req.Message)
	req.SystemPrompt = security.RedactSensitiveText(req.SystemPrompt)
	req.StaticSystemPrompt = security.RedactSensitiveText(req.StaticSystemPrompt)
	for i := range req.Documents {
		doc := &req.Documents[i]
		if strings.HasPrefix(doc.MimeType, "text/") || doc.MimeType == "application/json" {
			if utf8.Valid(doc.Data) {
				doc.Data = []byte(security.RedactSensitiveText(string(doc.Data)))
			}
		}
		// Preserve opaque and non-UTF8 attachments on existing channel paths.
		// Text masking makes no claim about binary/OCR contents. Telegram PDFs
		// already use the bounded local extraction tools instead of this path.
	}
	for i := range req.History {
		req.History[i].Content = security.RedactSensitiveText(req.History[i].Content)
	}
	for i := range req.ToolExchanges {
		ex := &req.ToolExchanges[i]
		ex.AssistantText = security.RedactSensitiveText(ex.AssistantText)
		ex.ReasoningContent = security.RedactSensitiveText(ex.ReasoningContent)
		for j := range ex.ToolCalls {
			ex.ToolCalls[j].Input = redactToolInput(ex.ToolCalls[j].Input)
		}
		for j := range ex.Results {
			ex.Results[j].Content = security.RedactSensitiveText(ex.Results[j].Content)
		}
	}
}

// Decode string values instead of editing serialized JSON: redaction must not
// turn prior tool-call arguments into malformed JSON on the next model round.
func redactToolInput(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return json.RawMessage(`{}`)
	}
	var walk func(any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case string:
			return security.RedactSensitiveText(x)
		case json.Number:
			if s := security.RedactPaymentData(x.String()); s != x.String() {
				return s
			}
			return x
		case []any:
			for i := range x {
				x[i] = walk(x[i])
			}
			return x
		case map[string]any:
			for k, v := range x {
				key := strings.ToLower(k)
				if key == "api_key" || key == "password" || key == "token" || key == "access_token" || key == "refresh_token" || key == "secret" {
					x[k] = "[REDACTED]"
				} else {
					x[k] = walk(v)
				}
			}
			return x
		default:
			return v
		}
	}
	safe, e := json.Marshal(walk(value))
	if e != nil {
		return json.RawMessage(`{}`)
	}
	return safe
}

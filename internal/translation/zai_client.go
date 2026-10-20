package translation

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/iulita-ai/iulita/internal/llm"
)

// normalize accepts the two explicitly documented messages shapes. It never
// guesses a translation from arbitrary strings or publishes partial output.
func normalize(data []byte) (Response, error) {
	var envelope struct {
		AgentID string          `json:"agent_id"`
		Status  *string         `json:"status"`
		Error   json.RawMessage `json:"error"`
		Choices []struct {
			Finish   string          `json:"finish_reason"`
			Messages json.RawMessage `json:"messages"`
		} `json:"choices"`
		Usage json.RawMessage `json:"usage"`
	}
	var out Response
	if !utf8.Valid(data) || json.Unmarshal(data, &envelope) != nil {
		return out, fail("protocol_error")
	}
	if len(envelope.Usage) > 0 && !bytes.Equal(bytes.TrimSpace(envelope.Usage), []byte("null")) {
		var usage struct {
			Input  *int64 `json:"prompt_tokens"`
			Output *int64 `json:"completion_tokens"`
			Total  *int64 `json:"total_tokens"`
			Calls  *int64 `json:"total_calls"`
		}
		if json.Unmarshal(envelope.Usage, &usage) != nil || usage.Input == nil || usage.Output == nil || *usage.Input < 0 || *usage.Output < 0 || *usage.Input > 1e12 || *usage.Output > 1e12 {
			return out, fail("protocol_error")
		}
		if usage.Total != nil && *usage.Total != *usage.Input+*usage.Output {
			return out, fail("protocol_error")
		}
		if usage.Calls != nil {
			if *usage.Calls < 0 || *usage.Calls > 1000 {
				return out, fail("protocol_error")
			}
			out.TotalCalls = *usage.Calls
		}
		out.Usage = llm.Usage{InputTokens: *usage.Input, OutputTokens: *usage.Output}
		out.UsageAvailable = true
	}
	if len(envelope.Error) > 0 && !bytes.Equal(bytes.TrimSpace(envelope.Error), []byte("null")) {
		return out, fail("provider_unavailable")
	}
	if envelope.AgentID != AgentID || len(envelope.Choices) != 1 {
		return out, fail("protocol_error")
	}
	if envelope.Status != nil && *envelope.Status != "completed" && *envelope.Status != "success" {
		return out, fail("incomplete_result")
	}
	if envelope.Choices[0].Finish != "stop" {
		return out, fail("incomplete_result")
	}
	raw := bytes.TrimSpace(envelope.Choices[0].Messages)
	var messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if len(raw) == 0 {
		return out, fail("protocol_error")
	}
	switch raw[0] {
	case '{':
		var m struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(raw, &m) != nil {
			return out, fail("protocol_error")
		}
		messages = append(messages, m)
	case '[':
		if json.Unmarshal(raw, &messages) != nil {
			return out, fail("protocol_error")
		}
	default:
		return out, fail("protocol_error")
	}
	if len(messages) != 1 || messages[0].Role != "assistant" {
		return out, fail("protocol_error")
	}
	var content textContent
	if json.Unmarshal(messages[0].Content, &content) != nil || content.Type != "text" || strings.TrimSpace(content.Text) == "" || !utf8.ValidString(content.Text) {
		return out, fail("protocol_error")
	}
	if utf8.RuneCountInString(content.Text) > maxOutputRunes {
		return out, fail("protocol_error")
	}
	out.Text = content.Text
	return out, nil
}

// Package translate exposes the native, terminal text translation operation.
package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/translation"
)

// Skill exposes the terminal translation service in the main conversation only.
type Skill struct{ service *translation.Service }

// New binds the skill to the shared translation service.
func New(service *translation.Service) *Skill { return &Skill{service: service} }

// Name returns the stable tool identity used by the registry.
func (*Skill) Name() string { return "translate" }

// Description explains tool selection and its exclusive terminal behavior.
func (*Skill) Description() string {
	return "Translate text with Z.ai Translation Agent. Requires an explicit target language. Returns the finished translation directly; use alone, never together with sending, explanation or other tools. Ask for the target first when missing."
}

// DefaultEnabled requires an explicit administrator opt-in.
func (*Skill) DefaultEnabled() bool { return false }

// TerminalResult prevents synthesis or additional tools after this operation.
func (*Skill) TerminalResult() bool { return true }

// MainLoopOnly excludes hidden paid calls from subagent and external runtimes.
func (*Skill) MainLoopOnly() bool { return true }

// Available checks the current connection without sending an API request.
func (s *Skill) Available() bool { return s.service.Status() == "ready" }

// ServiceStatus describes admission readiness without claiming tested entitlement.
func (s *Skill) ServiceStatus() skill.ServiceStatus {
	return skill.ServiceStatus{Provider: "zai", Service: translation.AgentID, State: s.service.Status(), Checked: false}
}

// SkipLedgerDedup keeps paid operations out of query-result replay paths.
func (*Skill) SkipLedgerDedup() bool { return true }

// SuppressResultPreview prevents translation content from entering preview logs.
func (*Skill) SuppressResultPreview() bool { return true }

// IsSensitiveOutput keeps translated text out of generated approval previews.
func (*Skill) IsSensitiveOutput() bool { return true }

// InputSchema exposes only source text and documented language codes.
func (*Skill) InputSchema() json.RawMessage {
	data, err := json.Marshal(map[string]any{"type": "object", "additionalProperties": false, "required": []string{"text", "target_language"}, "properties": map[string]any{
		"text":            map[string]any{"type": "string", "minLength": 1, "maxLength": translation.MaxTextRunes, "description": "Exact text to translate, preserving whitespace, URLs, numbers, placeholders and code."},
		"source_language": map[string]any{"type": "string", "enum": translation.SourceLanguages(), "description": "Documented source code; default auto."},
		"target_language": map[string]any{"type": "string", "enum": translation.TargetLanguages(), "description": "Explicit target language from the user request; never infer a default."},
	}})
	if err != nil {
		// A future non-JSON schema value must hide the tool, not emit bad JSON.
		return nil
	}
	return data
}

type input struct {
	Text   string `json:"text"`
	Source string `json:"source_language"`
	Target string `json:"target_language"`
}

func decode(data json.RawMessage) (input, error) {
	var in input
	if len(data) > 128<<10 || !utf8.Valid(data) {
		return in, &translation.Error{Code: "invalid_input"}
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil {
		return in, &translation.Error{Code: "invalid_input"}
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return in, &translation.Error{Code: "invalid_input"}
	}
	return in, nil
}

// ExecuteResult validates input and returns a trusted verbatim delivery result.
func (s *Skill) ExecuteResult(ctx context.Context, data json.RawMessage) (skill.Result, error) {
	in, err := decode(data)
	if err != nil {
		return skill.Result{}, err
	}
	resp, err := s.service.Translate(ctx, translation.Request{Text: in.Text, SourceLanguage: in.Source, TargetLanguage: in.Target, UserID: skill.UserIDFrom(ctx), ChatID: skill.ChatIDFrom(ctx)})
	if err != nil {
		return skill.Result{}, err
	}
	return skill.Result{Text: resp.Text, Verbatim: true}, nil
}

// Execute preserves the legacy interface; dispatchers call only one execution path.
func (s *Skill) Execute(ctx context.Context, data json.RawMessage) (string, error) {
	r, err := s.ExecuteResult(ctx, data)
	return r.Text, err
}

// SafeInputSummary describes only validated language codes and source length.
func (*Skill) SafeInputSummary(data json.RawMessage) string {
	in, err := decode(data)
	if err != nil {
		return "translation invalid input"
	}
	if in.Source == "" {
		in.Source = "auto"
	}
	// Only enum values can reach the summary, never caller-controlled labels.
	source, target := "?", "?"
	for _, v := range translation.SourceLanguages() {
		if in.Source == v {
			source = v
		}
	}
	for _, v := range translation.TargetLanguages() {
		if in.Target == v {
			target = v
		}
	}
	return fmt.Sprintf("translation %s→%s, chars=%d", source, target, utf8.RuneCountInString(in.Text))
}

var _ skill.ResultExecutor = (*Skill)(nil)
var _ skill.ExecutionPolicy = (*Skill)(nil)
var _ skill.SensitiveOutput = (*Skill)(nil)

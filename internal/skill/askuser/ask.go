// Package askuser provides a transport-independent typed clarification tool.
package askuser

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/skill/interact"
)

// Skill implements transport-independent clarifications.
type Skill struct{}

// New constructs the clarification skill.
func New() *Skill { return &Skill{} }

// Name identifies the clarification tool.
func (*Skill) Name() string { return "ask_user" }

// Description explains that clarification cannot grant action permissions.
func (*Skill) Description() string {
	return "Ask the user a clarification, optionally offering up to five choices. This is never a security approval or permission grant."
}

// InputSchema declares bounded text and optional choice labels.
func (*Skill) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"question":{"type":"string"},"options":{"type":"array","items":{"type":"string"}}},"required":["question"],"additionalProperties":false}`)
}

// RequiredCapabilities requires no external service capability.
func (*Skill) RequiredCapabilities() []string { return nil }

// NativePolicy declares the reviewed native actions and authority requirements.
func (*Skill) NativePolicy() skill.NativePolicy {
	return skill.NativePolicy{Revision: "typed-clarification-v1", Interactive: true, Actions: map[string]skill.ActionPolicy{"": {Effect: "read"}}}
}

// Execute asks through the current transport prompter.
func (*Skill) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		Question string   `json:"question"`
		Options  []string `json:"options"`
	}
	if json.Unmarshal(raw, &in) != nil || strings.TrimSpace(in.Question) == "" || len(in.Question) > 4096 || len(in.Options) > interact.MaxOptions {
		return `{"status":"invalid_input"}`, nil //nolint:nilerr // Invalid arguments are an explicit tool outcome; no action was dispatched.
	}
	var options []interact.Option
	for n, label := range in.Options {
		if strings.TrimSpace(label) == "" || len(label) > 1024 {
			return `{"status":"invalid_input"}`, nil
		}
		options = append(options, interact.Option{ID: strconv.Itoa(n), Label: label})
	}
	answer, err := interact.PrompterFrom(ctx).Ask(ctx, in.Question, options)
	if err != nil {
		return "", fmt.Errorf("clarification was not answered: %w", err)
	}
	// Keep the response explicit: a typed choice is an ID; free text is data.
	out, err := json.Marshal(struct {
		Answer  string   `json:"answer"`
		Options []string `json:"options,omitempty"`
	}{answer, in.Options})
	return string(out), err
}

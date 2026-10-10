package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/skill"
)

type nopSkill struct{}

func (nopSkill) Name() string        { return "nop" }
func (nopSkill) Description() string { return "nop skill" }
func (nopSkill) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (nopSkill) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}

func TestStaticSystemPromptIncludesFormattingDirective(t *testing.T) {
	reg := skill.NewRegistry()
	reg.RegisterWithManifest(nopSkill{}, &skill.Manifest{
		Name: "nop", SystemPrompt: "NOP SKILL RULES",
	})
	store := newTestStore(t)
	a := New(&funcProvider{fn: func(context.Context, llm.Request) (llm.Response, error) {
		return llm.Response{Content: "ok"}, nil
	}}, store, reg, "base prompt", "", 200000, zap.NewNop())

	got := a.staticSystemPrompt()
	if !strings.Contains(got, "base prompt") {
		t.Errorf("static prompt missing base: %q", got)
	}
	if !strings.Contains(got, "NOP SKILL RULES") {
		t.Errorf("static prompt missing skill rules: %q", got)
	}
	if !strings.Contains(got, "## Response Formatting") ||
		!strings.Contains(got, "inline code (backticks)") ||
		!strings.Contains(got, "one-tap copy") {
		t.Errorf("static prompt missing formatting directive: %q", got)
	}
}

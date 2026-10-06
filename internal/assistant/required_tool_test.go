package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/skill"
	"go.uber.org/zap"
)

type requiredFixtureSkill struct {
	err      error
	approval skill.ApprovalLevel
	calls    int
}

func (*requiredFixtureSkill) Name() string        { return "remember" }
func (*requiredFixtureSkill) Description() string { return "Save a memory" }
func (*requiredFixtureSkill) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (s *requiredFixtureSkill) Execute(context.Context, json.RawMessage) (string, error) {
	s.calls++
	return "saved", s.err
}
func (s *requiredFixtureSkill) ApprovalLevel() skill.ApprovalLevel { return s.approval }
func TestRequiredToolMustActuallySucceed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		call    bool
		failure bool
		pending bool
	}{
		{"ignored", false, false, false}, {"failed", true, true, false}, {"success", true, false, false}, {"approval", true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool := &requiredFixtureSkill{}
			if tc.failure {
				tool.err = errors.New("could not save")
			}
			if tc.pending {
				tool.approval = skill.ApprovalPrompt
			}
			calls := 0
			p := &funcProvider{fn: func(_ context.Context, req llm.Request) (llm.Response, error) {
				calls++
				if calls == 1 {
					if !req.RequireToolOutcome || req.ForceTool != "remember" {
						t.Fatal("engine outcome contract missing")
					}
					if tc.call {
						return llm.Response{ToolCalls: []llm.ToolCall{{ID: "call", Name: "remember", Input: json.RawMessage(`{}`)}}}, nil
					}
				}
				return llm.Response{Content: "saved successfully"}, nil
			}}
			reg := skill.NewRegistry()
			reg.Register(tool)
			a := New(p, newSynthTestStore(t), reg, "test", "", 200000, zap.NewNop())
			a.SetMemoryTriggers([]string{"remember"})
			out, err := a.HandleMessage(context.Background(), newTestMsg("chat", "remember this preference"))
			switch {
			case !tc.call || tc.failure:
				if err == nil || out != "" {
					t.Fatalf("false success: %q %v", out, err)
				}
			case tc.pending:
				if err != nil || calls != 1 || tool.calls != 0 || strings.Contains(out, "saved successfully") {
					t.Fatalf("approval treated as completed: %q %v calls%d", out, err, calls)
				}
			case err != nil || tool.calls != 1 || out != "saved successfully":
				t.Fatalf("success rejected: %q %v", out, err)
			}
		})
	}
}

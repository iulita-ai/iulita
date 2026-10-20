package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/skill/filesummary"
	"github.com/iulita-ai/iulita/internal/skill/pdfreader"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type documentPrivacyFixture struct {
	skill.Skill
	skill.ExecutionPolicy
}

func (documentPrivacyFixture) Execute(context.Context, json.RawMessage) (string, error) {
	return "PRIVATE_DOCUMENT_CANARY", nil
}

func TestDocumentSkillsDoNotLogContents(t *testing.T) {
	for _, tool := range []skill.Skill{pdfreader.New(), filesummary.New(nil, nil)} {
		t.Run(tool.Name(), func(t *testing.T) {
			policy, ok := tool.(skill.ExecutionPolicy)
			if !ok {
				t.Fatal("document skill must declare private execution policy")
			}
			if got := policy.SafeInputSummary(json.RawMessage(`{"filename":"PRIVATE_DOCUMENT_CANARY"}`)); strings.Contains(got, "PRIVATE_DOCUMENT_CANARY") {
				t.Fatal("metadata leaked")
			}
			reg := skill.NewRegistry()
			reg.SetCapabilities([]string{"memory"})
			reg.Register(documentPrivacyFixture{tool, policy})
			core, logs := observer.New(zap.InfoLevel)
			a := &Assistant{registry: reg, logger: zap.New(core)}
			result := a.executeSkill(context.Background(), "test-chat", llm.ToolCall{Name: tool.Name(), ID: "test-call", Input: json.RawMessage(`{}`)}, 0)
			if result.IsError || result.Content != "PRIVATE_DOCUMENT_CANARY" {
				t.Fatalf("tool output changed: %+v", result)
			}
			entries := logs.FilterMessage("skill execution result").All()
			if len(entries) != 1 {
				t.Fatalf("missing result log: %+v", entries)
			}
			for _, entry := range entries {
				if _, present := entry.ContextMap()["preview"]; present {
					t.Fatal("private output preview logged")
				}
				encoded, err := json.Marshal(entry.ContextMap())
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(encoded), "PRIVATE_DOCUMENT_CANARY") {
					t.Fatal("document contents leaked to logs")
				}
			}
		})
	}
}

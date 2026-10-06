package assistant

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/models"
	"github.com/iulita-ai/iulita/internal/skill"
	"go.uber.org/zap"
)

func TestLegacySynthesisHandoffContinuesToolsAndOverflowRetry(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		t.Run(map[bool]string{false: "tools", true: "overflow"}[overflow], func(t *testing.T) {
			settings := models.Settings{SchemaVersion: 1, Profiles: []models.Profile{
				{ID: "sonnet", Name: "Sonnet", Connection: "claude", Model: "claude-sonnet-4-6", MaxOutputTokens: 4096, Thinking: "disabled"},
				{ID: "haiku", Name: "Haiku", Connection: "claude", Model: "claude-haiku-4-5", MaxOutputTokens: 4096, Thinking: "disabled"},
			}, Policy: models.Policy{Everyday: "sonnet", Background: "haiku"}}
			sonnetCalls, haikuCalls := 0, 0
			sonnet := &funcProvider{fn: func(_ context.Context, req llm.Request) (llm.Response, error) {
				sonnetCalls++
				return llm.Response{ToolCalls: []llm.ToolCall{{ID: "first", Name: "cheap_tool", Input: json.RawMessage("{}")}}}, nil
			}}
			haiku := &funcProvider{fn: func(_ context.Context, req llm.Request) (llm.Response, error) {
				haikuCalls++
				for _, ex := range req.ToolExchanges {
					if ex.ProfileID != "haiku" {
						t.Fatalf("mixed origin after handoff: %q", ex.ProfileID)
					}
				}
				if haikuCalls == 1 {
					if overflow {
						return llm.Response{}, llm.ErrContextTooLarge
					}
					return llm.Response{ToolCalls: []llm.ToolCall{{ID: "second", Name: "cheap_tool", Input: json.RawMessage("{}")}}}, nil
				}
				return llm.Response{Content: "complete"}, nil
			}}
			bindings := map[string]llm.ProfileBinding{}
			for i, p := range settings.Profiles {
				var provider llm.Provider = sonnet
				if i == 1 {
					provider = haiku
				}
				bindings[p.ID] = llm.ProfileBinding{Profile: p, Provider: provider, Eligibility: "legacy_preserved", ContextTokens: 100000}
			}
			snapshot, err := llm.NewProfileSnapshot(1, settings, bindings)
			if err != nil {
				t.Fatal(err)
			}
			router := llm.NewRoutingProvider(nil, nil)
			if err = router.SetProfileSnapshot(snapshot); err != nil {
				t.Fatal(err)
			}
			reg := skill.NewRegistry()
			reg.Register(&cheapSkill{})
			a := New(router, newSynthTestStore(t), reg, "test", "", 200000, zap.NewNop())
			out, err := a.HandleMessage(context.Background(), newTestMsg("chat", "use cheap tool"))
			if err != nil || out != "complete" || sonnetCalls != 1 || haikuCalls != 2 {
				t.Fatalf("out=%q err=%v sonnet=%d haiku=%d", out, err, sonnetCalls, haikuCalls)
			}
			if got := a.compressionContextWindow(llm.Request{RoutingSnapshot: snapshot, ProfileID: "haiku"}); got != 100000 {
				t.Fatalf("wrong selected context window %d", got)
			}
		})
	}
}

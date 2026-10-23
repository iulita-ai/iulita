package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/i18n"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/mcpclient"
	"github.com/iulita-ai/iulita/internal/skill"
	"go.uber.org/zap"
)

type mcpGroundingSkill struct {
	name, output string
	err          error
	calls        int
}

func (s *mcpGroundingSkill) Name() string               { return s.name }
func (*mcpGroundingSkill) Description() string          { return "MCP fixture" }
func (*mcpGroundingSkill) InputSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (s *mcpGroundingSkill) Execute(context.Context, json.RawMessage) (string, error) {
	s.calls++
	return s.output, s.err
}
func (*mcpGroundingSkill) MCPGroundingNames(context.Context) []string {
	return []string{"SimAtlas", "search_esim_offers"}
}

func TestMCPRequestRequiresCurrentTurnEvidence(t *testing.T) {
	if err := i18n.Init(); err != nil {
		t.Fatal(err)
	}
	for _, metadataOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "no_calls", true: "catalog_only"}[metadataOnly], func(t *testing.T) {
			reg := skill.NewRegistry()
			reg.Register(&mcpGroundingSkill{name: "mcp_connections", output: `[{"catalog_count":7}]`})
			reg.Register(&mcpGroundingSkill{name: "mcp_call_tool"})
			providerCalls := 0
			provider := &funcProvider{fn: func(_ context.Context, req llm.Request) (llm.Response, error) {
				providerCalls++
				if !req.RequireToolOutcome || !strings.Contains(req.SystemPrompt, mcpEvidenceDirective) {
					t.Fatal("MCP evidence contract missing")
				}
				if metadataOnly && providerCalls == 1 {
					return llm.Response{ToolCalls: []llm.ToolCall{{ID: "catalog", Name: "mcp_connections", Input: json.RawMessage(`{}`)}}}, nil
				}
				return llm.Response{Content: "MCP succeeded, tariffs cost £16 and £32"}, nil
			}}
			store := newSynthTestStore(t)
			a := New(provider, store, reg, "test", "", 200000, zap.NewNop())
			// Prior assistant prose must not become evidence for this request.
			a.saveAssistantResponse(context.Background(), "chat", "MCP succeeded yesterday; tariffs cost £16")
			out, err := a.HandleMessage(context.Background(), newTestMsg("chat", "Выполни search_esim_offers у SimAtlas"))
			if err != nil || out != i18n.T(context.Background(), "MCPNoVerifiedOperation") || strings.Contains(out, "£") {
				t.Fatalf("invented result escaped: %q %v", out, err)
			}
			wantCalls := 2
			if metadataOnly {
				wantCalls = 3
			}
			if providerCalls != wantCalls {
				t.Fatalf("unbounded repair: %d", providerCalls)
			}
		})
	}
}

func TestMCPOutcomeStopsSynthesisAndAutomaticRetry(t *testing.T) {
	if err := i18n.Init(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, output, key string
		err               error
	}{
		{"expired", "", "MCPConfirmationExpired", mcpclient.ErrConfirmationExpired},
		{"declined", "", "MCPConfirmationRejected", mcpclient.ErrConfirmationRejected},
		{"cancelled", "", "MCPConfirmationCancelled", mcpclient.ErrConfirmationCancelled},
		{"unavailable", "", "MCPConfirmationUnavailable", mcpclient.ErrConfirmationUnavailable},
		{"private", `{"id":"execution-private","state":"succeeded","artifact_id":"artifact"}`, "MCPPrivateExecution", nil},
		{"pending", `{"state":"approval_pending","plan_id":"plan"}`, "MCPConfirmationUnavailable", nil},
		{"unknown", `{"id":"execution-unknown","state":"unknown_outcome"}`, "MCPExecutionUnverified", nil},
		{"malformed", `{"state":"succeeded","text":"made up prices"}`, "MCPExecutionUnverified", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := skill.NewRegistry()
			reg.Register(&mcpGroundingSkill{name: "mcp_connections"})
			tool := &mcpGroundingSkill{name: "mcp_call_tool", output: tc.output, err: tc.err}
			reg.Register(tool)
			calls := 0
			provider := &funcProvider{fn: func(context.Context, llm.Request) (llm.Response, error) {
				calls++
				if calls > 1 {
					t.Fatal("model allowed to reinterpret failure or retry")
				}
				return llm.Response{ToolCalls: []llm.ToolCall{{ID: "invoke", Name: "mcp_call_tool", Input: json.RawMessage(`{}`)}}}, nil
			}}
			a := New(provider, newSynthTestStore(t), reg, "test", "", 200000, zap.NewNop())
			out, err := a.HandleMessage(context.Background(), newTestMsg("chat", "mcp_call_tool"))
			if err != nil || !strings.HasPrefix(out, i18n.T(context.Background(), tc.key)) || tool.calls != 1 {
				t.Fatalf("wrong boundary: %q %v calls%d", out, err, tool.calls)
			}
		})
	}
}

func TestMCPProjectedSuccessAllowsSynthesis(t *testing.T) {
	reg := skill.NewRegistry()
	reg.Register(&mcpGroundingSkill{name: "mcp_connections"})
	reg.Register(&mcpGroundingSkill{name: "mcp_call_tool", output: `{"id":"execution","state":"succeeded","text":"30GB, 2112 RUB"}`})
	calls := 0
	provider := &funcProvider{fn: func(_ context.Context, req llm.Request) (llm.Response, error) {
		calls++
		if calls == 1 {
			return llm.Response{ToolCalls: []llm.ToolCall{{ID: "invoke", Name: "mcp_call_tool", Input: json.RawMessage(`{}`)}}}, nil
		}
		if !mcpRequirementMet("invocation", req.ToolExchanges) {
			t.Fatal("lost actual execution evidence")
		}
		return llm.Response{Content: "30GB, 2112 RUB"}, nil
	}}
	a := New(provider, newSynthTestStore(t), reg, "test", "", 200000, zap.NewNop())
	out, err := a.HandleMessage(context.Background(), newTestMsg("chat", "Выполни MCP поиск"))
	if err != nil || out != "30GB, 2112 RUB" || calls != 2 {
		t.Fatalf("valid result blocked: %q %v", out, err)
	}
}

func TestMCPRequirementsDoNotTurnGeneralQuestionsIntoOperations(t *testing.T) {
	reg := skill.NewRegistry()
	reg.Register(&mcpGroundingSkill{name: "mcp_connections"})
	a := New(nil, nil, reg, "", "", 0, zap.NewNop())
	tools := []llm.ToolDefinition{{Name: "mcp_connections"}, {Name: "mcp_call_tool"}, {Name: "mcp_search_tools"}}
	for _, tc := range []struct{ message, want string }{
		{"Как работает MCP?", ""}, {"Почему подтверждение MCP истекло?", ""}, {"Найди погоду", ""},
		{"Найди тариф через MCP", "invocation"}, {"Найди тариф у SimAtlas", "invocation"},
		{"Проверь SimAtlas", "metadata"}, {"Отключи MCP", "mcp_connection_action"},
		{"mcp_call_tool", "mcp_call_tool"},
		{"Заново получи mcp_connections и найди через mcp_search_tools search_esim_offers. Выполни только поиск.", "invocation"},
		{"Вызови mcp_search_tools с query=search_esim_offers", "mcp_search_tools"},
		{"Объясни mcp_call_tool", ""},
		{"Не вызывай mcp_call_tool", ""},
		{"How to invoke mcp_call_tool?", ""},
	} {
		if got := a.mcpRequirement(context.Background(), tc.message, tools); got != tc.want {
			t.Errorf("%q: %s want %s", tc.message, got, tc.want)
		}
	}
}

func TestMCPConfirmationErrorContract(t *testing.T) {
	var result map[string]any
	if err := json.Unmarshal([]byte(mcpErrorResult(errors.Join(errors.New("wrapped"), mcpclient.ErrConfirmationExpired))), &result); err != nil {
		t.Fatal(err)
	}
	if result["code"] != "confirmation_expired" || result["executed"] != false || result["permissions_changed"] != false || result["state"] != "not_started" {
		t.Fatal(result)
	}
}

func TestMCPRetryNeedsFreshEvidenceAndUsesOnlyPreviousUserRequest(t *testing.T) {
	reg := skill.NewRegistry()
	reg.Register(&mcpGroundingSkill{name: "mcp_connections"})
	a := New(nil, nil, reg, "", "", 0, zap.NewNop())
	tools := []llm.ToolDefinition{{Name: "mcp_connections"}, {Name: "mcp_call_tool"}}
	history := make([]domain.ChatMessage, 0, 4)
	history = append(history, domain.ChatMessage{Role: domain.RoleUser, Content: "Выполни search_esim_offers у SimAtlas"}, domain.ChatMessage{Role: domain.RoleAssistant, Content: "Confirmation expired; succeeded yesterday"})
	if got := a.mcpTurnRequirement(context.Background(), "Повтори!", tools, history); got != "invocation" {
		t.Fatal(got)
	}
	if mcpRequirementMet("invocation", nil) {
		t.Fatal("old conversation counted as execution evidence")
	}
	history = append(history, domain.ChatMessage{Role: domain.RoleUser, Content: "Расскажи анекдот"}, domain.ChatMessage{Role: domain.RoleAssistant, Content: "MCP succeeded"})
	if got := a.mcpTurnRequirement(context.Background(), "повтори", tools, history); got != "" {
		t.Fatal("unrelated follow-up forced MCP", got)
	}
}

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/i18n"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/mcpclient"
)

const mcpEvidenceDirective = `MCP evidence for this turn comes only from actual tool exchanges below, never from assistant text in conversation history. A catalog/descriptor/connection read does not execute a remote capability. A prepared or approved request is not a completed execution. Do not invent executions, artifact ids, prices or other result contents. Only a succeeded invocation with an execution id and nonempty text supplies result contents for synthesis. Confirmation expiry/refusal/cancellation is not missing permission or revoked OAuth. Do not retry a failed or unconfirmed invocation automatically.`

func (a *Assistant) mcpTurnRequirement(ctx context.Context, message string, tools []llm.ToolDefinition, history []domain.ChatMessage) string {
	if required := a.mcpRequirement(ctx, message, tools); required != "" {
		return required
	}
	switch strings.Trim(strings.ToLower(strings.TrimSpace(message)), ".!?") {
	case "повтори", "повтори запрос", "да, повтори", "сделай", "repeat", "try again":
		// A new user retry still needs fresh execution evidence and consent.
		// Only the immediately preceding user request determines its subject;
		// assistant claims in history never count as evidence or authorization.
		for index := len(history) - 1; index >= 0; index-- {
			if history[index].Role == domain.RoleUser {
				return a.mcpRequirement(ctx, history[index].Content, tools)
			}
		}
	}
	return ""
}

func isMCPInvocation(name string) bool {
	switch name {
	case "mcp_call_tool", "mcp_read_resource", "mcp_get_prompt":
		return true
	}
	return false
}

// mcpRequirement recognizes explicit operational requests, rather than trying to
// judge arbitrary prose produced by the model. General questions about MCP do
// not require running anything. Names come only from the owner's cached catalog.
func (a *Assistant) mcpRequirement(ctx context.Context, message string, tools []llm.ToolDefinition) string {
	available := map[string]bool{}
	for _, tool := range tools {
		available[tool.Name] = true
	}
	if !available["mcp_connections"] {
		return ""
	}
	text := strings.ToLower(message)
	trimmed := strings.TrimSpace(text)
	for _, prefix := range []string{"как ", "что ", "почему ", "объясни ", "не ", "how ", "what ", "why ", "explain ", "do not ", "don't "} {
		if strings.HasPrefix(trimmed, prefix) {
			return ""
		}
	}
	words := strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && r != '_' })
	has := func(options ...string) bool {
		for _, word := range words {
			for _, option := range options {
				if word == option {
					return true
				}
			}
		}
		return false
	}
	requested := ""
	for _, name := range []string{"mcp_call_tool", "mcp_read_resource", "mcp_get_prompt", "mcp_connection_action", "mcp_search_tools", "mcp_describe_tool", "mcp_connections", "mcp_resources", "mcp_prompts", "mcp_execution_status", "mcp_setup_link", "mcp_connection_draft", "mcp_cancel"} {
		if available[name] && has(name) {
			requested = name
			break
		}
	}
	operational := requested != "" && (trimmed == requested || strings.HasPrefix(trimmed, requested+":")) || has("вызови", "вызвать", "выполни", "выполнить", "запусти", "проверь", "найди", "покажи", "получи", "подключи", "отключи", "переподключи", "обнови", "call", "invoke", "execute", "run", "check", "find", "search", "show", "get", "connect", "disconnect", "reconnect", "refresh")
	if !operational {
		return ""
	}
	relevant := requested != "" || has("mcp")
	namedCapability := false
	if s, ok := a.registry.Get("mcp_connections"); ok {
		if names, ok := s.(interface {
			MCPGroundingNames(context.Context) []string
		}); ok {
			for _, name := range names.MCPGroundingNames(ctx) {
				if name != "" && strings.Contains(text, strings.ToLower(name)) {
					relevant = true
					if strings.Contains(name, "_") {
						namedCapability = true
					}
				}
			}
		}
	}
	if !relevant {
		return ""
	}
	if isMCPInvocation(requested) || requested == "mcp_connection_action" {
		return requested
	}
	if requested != "" {
		for _, verb := range []string{"вызови ", "вызвать ", "выполни ", "call ", "invoke ", "execute "} {
			if strings.Contains(text, verb+requested) {
				return requested
			}
		}
	}
	// A workflow may mention metadata dispatchers as prerequisite steps. They
	// cannot satisfy an explicit request to execute the named remote capability.
	if has("вызови", "вызвать", "выполни", "выполнить", "запусти", "invoke", "execute", "run") && (requested == "" || namedCapability) {
		return "invocation"
	}
	if requested != "" {
		return requested
	}
	if has("подключи", "отключи", "переподключи", "обнови", "connect", "disconnect", "reconnect", "refresh") {
		return "mcp_connection_action"
	}
	if has("каталог", "инструмент", "инструменты", "подключения", "catalog", "tools", "connections") || has("проверь", "check") {
		return "metadata"
	}
	return "invocation"
}

// Only results from this request count. Old conversation text and claimed tool
// calls cannot satisfy the requirement, and metadata cannot prove an invocation.
func mcpRequirementMet(required string, exchanges []llm.ToolExchange) bool {
	for _, exchange := range exchanges {
		for _, call := range exchange.ToolCalls {
			for _, result := range exchange.Results {
				if result.ToolCallID != call.ID || result.IsError || result.PendingApproval {
					continue
				}
				if required == "metadata" && strings.HasPrefix(call.Name, "mcp_") || call.Name == required {
					return true
				}
				if required == "invocation" && isMCPInvocation(call.Name) {
					var execution mcpclient.Execution
					if json.Unmarshal([]byte(result.Content), &execution) == nil && execution.ID != "" && execution.State == "succeeded" {
						return true
					}
				}
			}
		}
	}
	return false
}

func mcpErrorCode(err error) string {
	for _, candidate := range []struct {
		err  error
		code string
	}{
		{mcpclient.ErrConfirmationExpired, "confirmation_expired"},
		{mcpclient.ErrConfirmationRejected, "confirmation_rejected"},
		{mcpclient.ErrConfirmationCancelled, "confirmation_cancelled"},
		{mcpclient.ErrConfirmationUnavailable, "confirmation_unavailable"},
		{mcpclient.ErrDenied, "access_denied"},
		{mcpclient.ErrInvalid, "invalid_request"},
		{mcpclient.ErrConflict, "request_changed"},
		{mcpclient.ErrApproval, "confirmation_required"},
	} {
		if errors.Is(err, candidate.err) {
			return candidate.code
		}
	}
	return "request_failed"
}

func mcpErrorResult(err error) string {
	code := mcpErrorCode(err)
	result := map[string]any{"code": code, "execution_verified": false}
	if strings.HasPrefix(code, "confirmation_") {
		result["state"] = "not_started"
		result["executed"] = false
		result["permissions_changed"] = false
		result["next_action"] = "Ask the user whether to prepare a fresh exact confirmation. Do not retry automatically or change access/OAuth settings."
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return `{"code":"request_failed","execution_verified":false}`
	}
	return string(encoded)
}

// Stop before another model call on failed/unconfirmed or private invocations.
// This also prevents an automatic retry and protects private artifacts from
// being replaced by invented contents. Successful projected text may be synthesized.
func mcpOutcomeBoundary(ctx context.Context, name string, result llm.ToolResult) string {
	if !isMCPInvocation(name) && name != "mcp_connection_action" {
		return ""
	}
	if result.IsError {
		var failure struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal([]byte(result.Content), &failure); err != nil || failure.Code == "" {
			failure.Code = "request_failed"
		}
		switch failure.Code {
		case "confirmation_expired":
			return i18n.T(ctx, "MCPConfirmationExpired")
		case "confirmation_rejected":
			return i18n.T(ctx, "MCPConfirmationRejected")
		case "confirmation_cancelled":
			return i18n.T(ctx, "MCPConfirmationCancelled")
		case "confirmation_required", "confirmation_unavailable":
			return i18n.T(ctx, "MCPConfirmationUnavailable")
		default:
			return i18n.T(ctx, "MCPExecutionUnverified") + " (" + failure.Code + ")"
		}
	}
	if name == "mcp_connection_action" {
		return ""
	}
	var execution mcpclient.Execution
	if json.Unmarshal([]byte(result.Content), &execution) != nil {
		return i18n.T(ctx, "MCPExecutionUnverified")
	}
	if execution.State == "approval_pending" {
		return i18n.T(ctx, "MCPConfirmationUnavailable")
	}
	if execution.ID == "" || execution.State != "succeeded" {
		message := i18n.T(ctx, "MCPExecutionUnverified")
		if execution.ID != "" {
			message += " " + execution.ID + " (" + execution.State + ")"
		}
		return message
	}
	if execution.Text == "" {
		return i18n.T(ctx, "MCPPrivateExecution") + " " + execution.ID
	}
	return ""
}

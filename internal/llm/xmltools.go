package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var toolUseRegex = regexp.MustCompile(`<tool_use\s+name="([^"]+)">\s*<input>([\s\S]*?)</input>\s*</tool_use>`)

// XMLToolProvider wraps a Provider to enable tool calling via XML prompt injection.
// Used for providers that don't support native tool calling (Ollama, basic OpenAI-compatible).
type XMLToolProvider struct {
	inner Provider
}

// NewXMLToolProvider creates a new XML tool provider wrapper.
func NewXMLToolProvider(inner Provider) *XMLToolProvider {
	return &XMLToolProvider{inner: inner}
}

// Complete handles tool calling via XML injection if tools are present.
func (p *XMLToolProvider) Complete(ctx context.Context, req Request) (Response, error) {
	if len(req.Tools) == 0 {
		return p.inner.Complete(ctx, req)
	}

	// Build tool descriptions as XML and append to system prompt.
	var toolXML strings.Builder
	toolXML.WriteString("\n\n<available_tools>\n")
	for _, t := range req.Tools {
		toolXML.WriteString(fmt.Sprintf("<tool name=%q>\n", t.Name))
		toolXML.WriteString(fmt.Sprintf("<description>%s</description>\n", t.Description))
		toolXML.WriteString(fmt.Sprintf("<parameters>%s</parameters>\n", string(t.InputSchema)))
		toolXML.WriteString("</tool>\n")
	}
	toolXML.WriteString("</available_tools>\n\n")
	toolXML.WriteString("To use a tool, respond with: <tool_use name=\"tool_name\"><input>{\"key\":\"value\"}</input></tool_use>\n")
	toolXML.WriteString("You can use multiple tools. After tool results, continue your response.\n")

	// Modify request: inject XML into system prompt, strip native tools.
	modReq := req
	modReq.SystemPrompt = req.SystemPrompt + toolXML.String()
	modReq.Tools = nil

	resp, err := p.inner.Complete(ctx, modReq)
	if err != nil {
		return resp, err
	}

	// Parse response content for tool_use XML patterns.
	matches := toolUseRegex.FindAllStringSubmatch(resp.Content, -1)
	if len(matches) > 0 {
		for i, match := range matches {
			toolName := match[1]
			inputStr := strings.TrimSpace(match[2])

			// Validate JSON input.
			var inputJSON json.RawMessage
			if err := json.Unmarshal([]byte(inputStr), &inputJSON); err != nil {
				// If input is not valid JSON, wrap it as a string.
				inputJSON, _ = json.Marshal(map[string]string{"input": inputStr})
			}

			resp.ToolCalls = append(resp.ToolCalls, ToolCall{
				ID:    fmt.Sprintf("xmltool_%d", i+1),
				Name:  toolName,
				Input: inputJSON,
			})
		}

		// Strip tool_use tags from content.
		resp.Content = toolUseRegex.ReplaceAllString(resp.Content, "")
		resp.Content = strings.TrimSpace(resp.Content)
	}

	return resp, nil
}

// CompleteStream delegates to inner if it supports streaming, otherwise falls back to Complete.
func (p *XMLToolProvider) CompleteStream(ctx context.Context, req Request, callback StreamCallback) (Response, error) {
	if sp, ok := p.inner.(StreamingProvider); ok {
		return sp.CompleteStream(ctx, req, callback)
	}
	return p.Complete(ctx, req)
}

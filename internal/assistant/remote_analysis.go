package assistant

import "github.com/iulita-ai/iulita/internal/llm"

// Remote output is untrusted even after redaction. Restrict subsequent tools
// server-side so injected text cannot write memory, send data or delegate work.
func remoteAnalysisToolAllowed(name string) bool {
	switch name {
	case "remote_exec", "remote_chain", "remote_status", "remote_cancel", "datetime", "ask_user":
		return true
	}
	return false
}

func remoteAnalysisTools(tools []llm.ToolDefinition) []llm.ToolDefinition {
	out := make([]llm.ToolDefinition, 0, len(tools))
	for _, tool := range tools {
		if remoteAnalysisToolAllowed(tool.Name) {
			out = append(out, tool)
		}
	}
	return out
}

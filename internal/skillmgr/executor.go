package skillmgr

import (
	"context"

	"github.com/iulita-ai/iulita/internal/skill"
)

// Executor runs external skill code in an isolated environment.
type Executor interface {
	// Execute runs a command in the skill's isolation context.
	// installDir is the path to the extracted skill directory.
	// entrypoint is the main script (e.g. "main.py").
	// input is the JSON input from the LLM tool call.
	// env contains environment variables to pass to the process.
	// Returns the captured stdout output.
	Execute(ctx context.Context, installDir, entrypoint string, input []byte, env map[string]string) (string, error)

	// ApprovalLevel returns the required approval level for this executor.
	ApprovalLevel() skill.ApprovalLevel

	// IsolationLevel returns the isolation type string (e.g. "docker", "wasm").
	IsolationLevel() string

	// Available reports whether this executor can run on the current system.
	Available() bool
}

package shellexec

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/iulita-ai/iulita/internal/skill"
)

type Config struct {
	AllowedBins    []string
	Timeout        time.Duration
	ForbiddenPaths []string // paths that must not appear in command arguments
	WorkspaceDir   string   // if set, commands execute in this directory only
}

type shellExecInput struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// Skill executes whitelisted shell commands in a sandboxed environment.
type Skill struct {
	allowedBins    map[string]struct{}
	timeout        time.Duration
	forbiddenPaths []string
	workspaceDir   string
}

// ApprovalLevel implements skill.ApprovalDeclarer — shell exec requires admin approval.
func (s *Skill) ApprovalLevel() skill.ApprovalLevel { return skill.ApprovalManual }

func New(cfg Config) *Skill {
	allowed := make(map[string]struct{}, len(cfg.AllowedBins))
	for _, b := range cfg.AllowedBins {
		allowed[b] = struct{}{}
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	// Expand ~ in forbidden paths and normalize.
	forbidden := make([]string, 0, len(cfg.ForbiddenPaths))
	for _, p := range cfg.ForbiddenPaths {
		abs, err := filepath.Abs(p)
		if err == nil {
			forbidden = append(forbidden, abs)
		} else {
			forbidden = append(forbidden, p)
		}
	}
	return &Skill{
		allowedBins:    allowed,
		timeout:        timeout,
		forbiddenPaths: forbidden,
		workspaceDir:   cfg.WorkspaceDir,
	}
}

func (s *Skill) Name() string { return "shell_exec" }

func (s *Skill) Description() string {
	bins := make([]string, 0, len(s.allowedBins))
	for b := range s.allowedBins {
		bins = append(bins, b)
	}
	return fmt.Sprintf("Execute a shell command. Allowed commands: %s", strings.Join(bins, ", "))
}

func (s *Skill) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"properties": {
			"command": {"type": "string", "description": "The command to execute (must be in the allowed list)"},
			"args": {"type": "array", "items": {"type": "string"}, "description": "Command arguments"}
		},
		"required": ["command"]
	}`)
}

const maxOutputSize = 16 * 1024

func (s *Skill) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var inp shellExecInput
	if err := json.Unmarshal(input, &inp); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	if inp.Command == "" {
		return "", fmt.Errorf("command is required")
	}

	// Whitelist check — only the base command name, no paths.
	baseName := inp.Command
	if idx := strings.LastIndex(baseName, "/"); idx >= 0 {
		baseName = baseName[idx+1:]
	}
	if _, ok := s.allowedBins[baseName]; !ok {
		return "", fmt.Errorf("command %q is not in the allowed list", baseName)
	}

	// Check forbidden paths in arguments.
	if err := s.checkForbiddenPaths(inp.Args); err != nil {
		return "", err
	}

	execCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, inp.Command, inp.Args...)
	if s.workspaceDir != "" {
		cmd.Dir = s.workspaceDir
	} else {
		cmd.Dir = os.TempDir()
	}

	output, err := cmd.CombinedOutput()

	result := string(output)
	if len(result) > maxOutputSize {
		result = result[:maxOutputSize] + "\n... (output truncated)"
	}

	if err != nil {
		return fmt.Sprintf("error: %v\n%s", err, result), nil
	}

	return result, nil
}

// checkForbiddenPaths validates that no argument references a forbidden path.
func (s *Skill) checkForbiddenPaths(args []string) error {
	if len(s.forbiddenPaths) == 0 {
		return nil
	}
	for _, arg := range args {
		// Resolve to absolute path for comparison.
		abs, err := filepath.Abs(arg)
		if err != nil {
			abs = arg
		}
		for _, forbidden := range s.forbiddenPaths {
			if strings.HasPrefix(abs, forbidden) || strings.Contains(arg, "..") {
				return fmt.Errorf("access to path %q is forbidden", arg)
			}
		}
	}
	return nil
}

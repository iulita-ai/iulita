package skillmgr

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/skill"
)

var validEnvKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

const maxOutputSize = 16 << 10 // 16 KB

// DockerExecutor runs external skill code inside Docker containers.
type DockerExecutor struct {
	cfg config.DockerConfig
}

// NewDockerExecutor creates a Docker-based executor.
func NewDockerExecutor(cfg config.DockerConfig) *DockerExecutor {
	return &DockerExecutor{cfg: cfg}
}

func (e *DockerExecutor) IsolationLevel() string             { return "docker" }
func (e *DockerExecutor) ApprovalLevel() skill.ApprovalLevel { return skill.ApprovalManual }

// Available checks if Docker daemon is accessible.
func (e *DockerExecutor) Available() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "info")
	return cmd.Run() == nil
}

// Execute runs a command inside a Docker container with security constraints.
func (e *DockerExecutor) Execute(ctx context.Context, installDir, entrypoint string, input []byte, env map[string]string) (string, error) {
	timeout := 30 * time.Second
	if e.cfg.Timeout != "" {
		if d, err := time.ParseDuration(e.cfg.Timeout); err == nil {
			timeout = d
		}
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	image := e.cfg.Image
	if image == "" {
		image = "python:3.12-slim"
	}

	memLimit := e.cfg.MemoryLimit
	if memLimit == "" {
		memLimit = "256m"
	}

	cpuLimit := e.cfg.CPULimit
	if cpuLimit == "" {
		cpuLimit = "0.5"
	}

	args := []string{
		"run", "--rm",
		"--network", "none",
		"--memory", memLimit,
		"--cpus", cpuLimit,
		"--read-only",
		"--tmpfs", "/tmp:size=50m",
		"--user", "nobody",
		"-v", installDir + ":/skill:ro",
		"-w", "/skill",
	}

	// Pass environment variables (validated keys only).
	for k, v := range env {
		if !validEnvKeyRe.MatchString(k) {
			return "", fmt.Errorf("invalid env key: %q", k)
		}
		if strings.ContainsAny(v, "\x00") {
			return "", fmt.Errorf("env value for %q contains null byte", k)
		}
		args = append(args, "-e", k+"="+v)
	}

	// Determine the command to run.
	if entrypoint == "" {
		return "", fmt.Errorf("no entrypoint specified")
	}

	args = append(args, image)

	// Auto-detect runtime from entrypoint extension.
	switch {
	case strings.HasSuffix(entrypoint, ".py"):
		args = append(args, "python3", entrypoint)
	case strings.HasSuffix(entrypoint, ".js"):
		args = append(args, "node", entrypoint)
	case strings.HasSuffix(entrypoint, ".sh"):
		args = append(args, "sh", entrypoint)
	default:
		args = append(args, entrypoint)
	}

	cmd := exec.CommandContext(ctx, "docker", args...)

	// Pass input via stdin.
	if len(input) > 0 {
		cmd.Stdin = bytes.NewReader(input)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := stderr.String()
		if len(errMsg) > 1024 {
			errMsg = errMsg[:1024]
		}
		return "", fmt.Errorf("docker execution failed: %w\nstderr: %s", err, errMsg)
	}

	output := stdout.String()
	if len(output) > maxOutputSize {
		output = output[:maxOutputSize] + "\n... (truncated)"
	}

	return output, nil
}

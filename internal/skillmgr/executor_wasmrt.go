package skillmgr

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// WASMExecutor runs external skills in a WASM sandbox using wazero.
type WASMExecutor struct {
	runtime wazero.Runtime
}

// NewWASMExecutor creates a WASM-based executor.
func NewWASMExecutor(ctx context.Context) *WASMExecutor {
	r := wazero.NewRuntime(ctx)
	wasi_snapshot_preview1.MustInstantiate(ctx, r)
	return &WASMExecutor{runtime: r}
}

func (e *WASMExecutor) IsolationLevel() string             { return "wasm" }
func (e *WASMExecutor) ApprovalLevel() skill.ApprovalLevel { return skill.ApprovalPrompt }
func (e *WASMExecutor) Available() bool                    { return true } // Pure Go, always available.

// Execute runs a .wasm module from the skill's install directory.
func (e *WASMExecutor) Execute(ctx context.Context, installDir, entrypoint string, input []byte, env map[string]string) (string, error) {
	if entrypoint == "" {
		entrypoint = "main.wasm"
	}

	wasmPath := filepath.Join(installDir, entrypoint)
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		return "", fmt.Errorf("read wasm module %q: %w", wasmPath, err)
	}

	compiled, err := e.runtime.CompileModule(ctx, wasmBytes)
	if err != nil {
		return "", fmt.Errorf("compile wasm: %w", err)
	}
	defer compiled.Close(ctx)

	var stdout bytes.Buffer
	stdin := bytes.NewReader(input)

	moduleCfg := wazero.NewModuleConfig().
		WithStdin(stdin).
		WithStdout(&stdout).
		WithStderr(&stdout). // Capture stderr to stdout.
		WithName(filepath.Base(installDir))

	// Pass environment variables.
	for k, v := range env {
		moduleCfg = moduleCfg.WithEnv(k, v)
	}

	mod, err := e.runtime.InstantiateModule(ctx, compiled, moduleCfg)
	if err != nil {
		return "", fmt.Errorf("run wasm module: %w", err)
	}
	defer mod.Close(ctx)

	output := stdout.String()
	if len(output) > maxOutputSize {
		output = output[:maxOutputSize] + "\n... (truncated)"
	}

	return output, nil
}

// Close releases the WASM runtime resources.
func (e *WASMExecutor) Close(ctx context.Context) error {
	if e.runtime != nil {
		return e.runtime.Close(ctx)
	}
	return nil
}

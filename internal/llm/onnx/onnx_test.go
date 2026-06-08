package onnx

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocalModelPath(t *testing.T) {
	tests := []struct {
		name     string
		modelDir string
		model    string
		want     string
	}{
		{"default model", "/data/models", "KnightsAnalytics/all-MiniLM-L6-v2", "/data/models/KnightsAnalytics_all-MiniLM-L6-v2"},
		{"strips revision suffix", "/m", "org/model:main", "/m/org_model"},
		{"no slash", "/m", "plainmodel", "/m/plainmodel"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := localModelPath(tt.modelDir, tt.model); got != tt.want {
				t.Errorf("localModelPath(%q, %q) = %q, want %q", tt.modelDir, tt.model, got, tt.want)
			}
		})
	}
}

func TestIsModelCached(t *testing.T) {
	dir := t.TempDir()

	if isModelCached(dir) {
		t.Error("empty dir reported as cached")
	}

	// Only one of the two required files present → not cached.
	writeFile(t, filepath.Join(dir, "model.onnx"))
	if isModelCached(dir) {
		t.Error("dir with only model.onnx reported as cached")
	}

	// Both required files present → cached.
	writeFile(t, filepath.Join(dir, "tokenizer.json"))
	if !isModelCached(dir) {
		t.Error("dir with model.onnx + tokenizer.json not reported as cached")
	}
}

func TestPreloadModelPath(t *testing.T) {
	model := DefaultModel

	// Env unset → empty.
	t.Setenv(preloadDirEnv, "")
	if got := preloadModelPath(model); got != "" {
		t.Errorf("unset env: got %q, want \"\"", got)
	}

	// Env set but model absent → empty.
	dir := t.TempDir()
	t.Setenv(preloadDirEnv, dir)
	if got := preloadModelPath(model); got != "" {
		t.Errorf("env set, model absent: got %q, want \"\"", got)
	}

	// Env set and model present → path.
	modelPath := localModelPath(dir, model)
	if err := os.MkdirAll(modelPath, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(modelPath, "model.onnx"))
	writeFile(t, filepath.Join(modelPath, "tokenizer.json"))
	if got := preloadModelPath(model); got != modelPath {
		t.Errorf("env set, model present: got %q, want %q", got, modelPath)
	}
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

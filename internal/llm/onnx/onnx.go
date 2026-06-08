package onnx

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"go.uber.org/zap"

	"github.com/knights-analytics/hugot"
	"github.com/knights-analytics/hugot/pipelines"
)

// Ensure Provider implements llm.EmbeddingProvider.
var _ interface {
	Embed(context.Context, []string) ([][]float32, error)
	Dimensions() int
} = (*Provider)(nil)

const (
	// DefaultModel is the HuggingFace model ID for sentence embeddings.
	DefaultModel = "KnightsAnalytics/all-MiniLM-L6-v2"
	// dimensions produced by all-MiniLM-L6-v2.
	defaultDimensions = 384
	// preloadDirEnv names the env var pointing at a directory holding a model
	// baked into the container image, so startup needs no network. Read directly
	// (not via koanf) to sidestep the env-key underscore mangling.
	preloadDirEnv = "IULITA_MODEL_PRELOAD_DIR"
)

// Provider implements llm.EmbeddingProvider using hugot's pure Go backend.
type Provider struct {
	session  *hugot.Session
	pipeline *pipelines.FeatureExtractionPipeline
	dims     int
	mu       sync.Mutex
	logger   *zap.Logger
}

// New creates a new ONNX embedding provider.
// modelDir is the directory to download/cache the model.
// model is the HuggingFace model ID (empty = DefaultModel).
func New(modelDir, model string, logger *zap.Logger) (*Provider, error) {
	if model == "" {
		model = DefaultModel
	}

	// Ensure model directory exists.
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating model directory %s: %w", modelDir, err)
	}

	logger.Info("initializing ONNX embedding provider",
		zap.String("model", model),
		zap.String("model_dir", modelDir),
	)

	// Create pure Go session (no CGo, no ONNX Runtime shared library).
	session, err := hugot.NewGoSession()
	if err != nil {
		return nil, fmt.Errorf("creating hugot session: %w", err)
	}

	// Resolve the model path and avoid the network download whenever possible.
	// hugot.DownloadModel always re-validates against the HuggingFace hub cache
	// (network), so without these guards every restart — and every pod restart
	// with an ephemeral hub cache — re-fetches the model.
	//
	//  1. Already present in modelDir (persisted on the data volume) → use it.
	//  2. Baked into the image at IULITA_MODEL_PRELOAD_DIR → use it directly
	//     (read-only inference, no copy), independent of where modelDir resolves.
	//  3. Otherwise download into modelDir.
	modelPath := localModelPath(modelDir, model)
	switch {
	case isModelCached(modelPath):
		logger.Info("embedding model already present, skipping download", zap.String("path", modelPath))
	case preloadModelPath(model) != "":
		modelPath = preloadModelPath(model)
		logger.Info("using preloaded embedding model", zap.String("path", modelPath))
	default:
		logger.Info("downloading/verifying embedding model (first run may take a minute)...")
		modelPath, err = hugot.DownloadModel(model, modelDir, hugot.NewDownloadOptions())
		if err != nil {
			session.Destroy() //nolint:errcheck,gosec // best-effort cleanup on the error path
			return nil, fmt.Errorf("downloading model %s to %s: %w", model, modelDir, err)
		}
		logger.Info("embedding model ready", zap.String("path", modelPath))
	}

	// Create feature extraction pipeline with L2 normalization for cosine similarity.
	config := hugot.FeatureExtractionConfig{
		ModelPath: modelPath,
		Name:      "embedder",
	}
	pipeline, err := hugot.NewPipeline(session, config)
	if err != nil {
		session.Destroy() //nolint:errcheck,gosec // best-effort cleanup on the error path
		return nil, fmt.Errorf("creating embedding pipeline: %w", err)
	}

	return &Provider{
		session:  session,
		pipeline: pipeline,
		dims:     defaultDimensions,
		logger:   logger,
	}, nil
}

// localModelPath returns the on-disk directory hugot.DownloadModel uses for a
// model inside modelDir, mirroring its naming (repo "/" replaced with "_", any
// ":revision" suffix stripped). Kept in sync with hugot's downloader.
func localModelPath(modelDir, model string) string {
	name := model
	if i := strings.IndexByte(name, ':'); i >= 0 {
		name = name[:i]
	}
	return filepath.Join(modelDir, strings.ReplaceAll(name, "/", "_"))
}

// preloadModelPath returns the path to the model baked into the image under
// IULITA_MODEL_PRELOAD_DIR, or "" if that env is unset or the model isn't there.
func preloadModelPath(model string) string {
	dir := os.Getenv(preloadDirEnv)
	if dir == "" {
		return ""
	}
	path := localModelPath(dir, model)
	if !isModelCached(path) {
		return ""
	}
	return path
}

// isModelCached reports whether the core model files are already present in
// modelPath, so the network download can be skipped.
func isModelCached(modelPath string) bool {
	for _, f := range []string{"model.onnx", "tokenizer.json"} {
		if _, err := os.Stat(filepath.Join(modelPath, f)); err != nil {
			return false
		}
	}
	return true
}

// Download fetches the model into modelDir (HuggingFace model ID; empty =
// DefaultModel) without constructing a pipeline. Used by the `download-model`
// subcommand to pre-warm the model at image-build time so startup is offline.
func Download(modelDir, model string, logger *zap.Logger) (string, error) {
	if model == "" {
		model = DefaultModel
	}
	if err := os.MkdirAll(modelDir, 0o750); err != nil {
		return "", fmt.Errorf("creating model directory %s: %w", modelDir, err)
	}
	if path := localModelPath(modelDir, model); isModelCached(path) {
		logger.Info("embedding model already present, skipping download", zap.String("path", path))
		return path, nil
	}
	logger.Info("downloading embedding model", zap.String("model", model), zap.String("model_dir", modelDir))
	modelPath, err := hugot.DownloadModel(model, modelDir, hugot.NewDownloadOptions())
	if err != nil {
		return "", fmt.Errorf("downloading model %s to %s: %w", model, modelDir, err)
	}
	logger.Info("embedding model downloaded", zap.String("path", modelPath))
	return modelPath, nil
}

// Embed generates embeddings for the given texts.
func (p *Provider) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	// hugot pipeline is not thread-safe, serialize access.
	p.mu.Lock()
	defer p.mu.Unlock()

	result, err := p.pipeline.RunPipeline(texts)
	if err != nil {
		return nil, fmt.Errorf("running embedding pipeline: %w", err)
	}

	return result.Embeddings, nil
}

// Dimensions returns the embedding dimensionality.
func (p *Provider) Dimensions() int {
	return p.dims
}

// Close releases resources.
func (p *Provider) Close() {
	if p.session != nil {
		p.session.Destroy()
	}
}

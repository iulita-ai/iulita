package onnx

import (
	"context"
	"fmt"
	"os"
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

	// Download model if not already cached.
	logger.Info("downloading/verifying embedding model (first run may take a minute)...")
	modelPath, err := hugot.DownloadModel(model, modelDir, hugot.NewDownloadOptions())
	if err != nil {
		session.Destroy()
		return nil, fmt.Errorf("downloading model %s to %s: %w", model, modelDir, err)
	}
	logger.Info("embedding model ready", zap.String("path", modelPath))

	// Create feature extraction pipeline with L2 normalization for cosine similarity.
	config := hugot.FeatureExtractionConfig{
		ModelPath: modelPath,
		Name:      "embedder",
	}
	pipeline, err := hugot.NewPipeline(session, config)
	if err != nil {
		session.Destroy()
		return nil, fmt.Errorf("creating embedding pipeline: %w", err)
	}

	return &Provider{
		session:  session,
		pipeline: pipeline,
		dims:     defaultDimensions,
		logger:   logger,
	}, nil
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

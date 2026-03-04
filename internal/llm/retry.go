package llm

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"net/http"
	"time"
)

// RetryConfig configures the retry behavior.
type RetryConfig struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

// DefaultRetryConfig returns sensible defaults.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts: 3,
		BaseDelay:   500 * time.Millisecond,
		MaxDelay:    8 * time.Second,
	}
}

// RetryProvider wraps a Provider with retry logic for transient errors.
type RetryProvider struct {
	inner  Provider
	config RetryConfig
}

// NewRetryProvider wraps inner with retry-on-failure logic.
func NewRetryProvider(inner Provider, config RetryConfig) *RetryProvider {
	if config.MaxAttempts <= 0 {
		config.MaxAttempts = 3
	}
	if config.BaseDelay <= 0 {
		config.BaseDelay = 500 * time.Millisecond
	}
	if config.MaxDelay <= 0 {
		config.MaxDelay = 8 * time.Second
	}
	return &RetryProvider{inner: inner, config: config}
}

func (p *RetryProvider) Complete(ctx context.Context, req Request) (Response, error) {
	var lastErr error
	for attempt := range p.config.MaxAttempts {
		resp, err := p.inner.Complete(ctx, req)
		if err == nil {
			return resp, nil
		}
		if !isRetryable(err) {
			return resp, err
		}
		lastErr = err

		if attempt < p.config.MaxAttempts-1 {
			delay := p.backoffDelay(attempt)
			select {
			case <-ctx.Done():
				return Response{}, ctx.Err()
			case <-time.After(delay):
			}
		}
	}
	return Response{}, lastErr
}

// CompleteStream delegates to inner if it supports streaming, otherwise falls back to Complete.
func (p *RetryProvider) CompleteStream(ctx context.Context, req Request, callback StreamCallback) (Response, error) {
	if sp, ok := p.inner.(StreamingProvider); ok {
		return sp.CompleteStream(ctx, req, callback)
	}
	return p.Complete(ctx, req)
}

func (p *RetryProvider) backoffDelay(attempt int) time.Duration {
	delay := float64(p.config.BaseDelay) * math.Pow(2, float64(attempt))
	// Jitter: 0.5 to 1.5x
	jitter := 0.5 + rand.Float64()
	delay *= jitter
	if delay > float64(p.config.MaxDelay) {
		delay = float64(p.config.MaxDelay)
	}
	return time.Duration(delay)
}

// HTTPStatusError is an interface for errors that carry an HTTP status code.
type HTTPStatusError interface {
	StatusCode() int
}

func isRetryable(err error) bool {
	var httpErr HTTPStatusError
	if errors.As(err, &httpErr) {
		code := httpErr.StatusCode()
		return code == http.StatusTooManyRequests ||
			code == http.StatusInternalServerError ||
			code == http.StatusBadGateway ||
			code == http.StatusServiceUnavailable ||
			code == 529 // Anthropic overloaded
	}
	return false
}

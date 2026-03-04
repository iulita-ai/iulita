package assistant

import (
	"context"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/channel"
)

// PreprocessorHook runs before the LLM call, modifying the incoming message.
type PreprocessorHook interface {
	Name() string
	Process(ctx context.Context, msg *channel.IncomingMessage) error
}

// PostprocessorHook runs after the LLM call, modifying the response text.
type PostprocessorHook interface {
	Name() string
	Process(ctx context.Context, response *string) error
}

// runPreHooks executes all registered preprocessor hooks in order.
func (a *Assistant) runPreHooks(ctx context.Context, msg *channel.IncomingMessage) {
	for _, h := range a.preHooks {
		if err := h.Process(ctx, msg); err != nil {
			a.logger.Warn("pre-hook failed", zap.String("hook", h.Name()), zap.Error(err))
		}
	}
}

// runPostHooks executes all registered postprocessor hooks in order.
func (a *Assistant) runPostHooks(ctx context.Context, response *string) {
	for _, h := range a.postHooks {
		if err := h.Process(ctx, response); err != nil {
			a.logger.Warn("post-hook failed", zap.String("hook", h.Name()), zap.Error(err))
		}
	}
}

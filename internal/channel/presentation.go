package channel

import (
	"context"
	"sync/atomic"
)

type responsePresentationKey struct{}

// ResponsePresentation is a trusted, request-local delivery instruction. It is
// never decoded from provider text or persisted in conversation history.
type ResponsePresentation struct {
	verbatim atomic.Bool
}

// WithResponsePresentation creates a fresh delivery carrier for one handler
// invocation. Context derivation preserves the carrier shared with the channel.
func WithResponsePresentation(ctx context.Context) (context.Context, *ResponsePresentation) {
	presentation := &ResponsePresentation{}
	return context.WithValue(ctx, responsePresentationKey{}, presentation), presentation
}

// MarkVerbatimResponse marks a validated terminal result for literal delivery.
// It has no effect when the channel did not install a request-local carrier.
func MarkVerbatimResponse(ctx context.Context) {
	if presentation, ok := ctx.Value(responsePresentationKey{}).(*ResponsePresentation); ok {
		presentation.verbatim.Store(true)
	}
}

// Verbatim reports whether this handler's final response requires literal text.
func (p *ResponsePresentation) Verbatim() bool {
	return p != nil && p.verbatim.Load()
}

// VerbatimChunks splits text on rune boundaries without adding, trimming or
// deleting characters. The limit counts UTF-16 code units, so supplementary
// Unicode characters remain within messaging APIs' character limits too.
func VerbatimChunks(text string, maxUTF16Units int) []string {
	if text == "" || maxUTF16Units < 2 {
		return nil
	}
	var chunks []string
	start, units := 0, 0
	for index, r := range text {
		width := 1
		if r > 0xffff {
			width = 2
		}
		if units+width > maxUTF16Units {
			chunks = append(chunks, text[start:index])
			start, units = index, 0
		}
		units += width
	}
	return append(chunks, text[start:])
}

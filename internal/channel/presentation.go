package channel

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/iulita-ai/iulita/internal/domain"
)

type responsePresentationKey struct{}

// ResponsePresentation is a trusted, request-local delivery instruction. It is
// never decoded from provider text or persisted in conversation history.
type ResponsePresentation struct {
	verbatim  atomic.Bool
	weatherMu sync.RWMutex
	weather   *domain.WeatherCard
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

// HasResponsePresentation identifies a channel-owned turn. Buffered transports
// leave final publication to that channel, after post-processing and persistence.
func HasResponsePresentation(ctx context.Context) bool {
	_, ok := ctx.Value(responsePresentationKey{}).(*ResponsePresentation)
	return ok
}

// SetWeatherCard accepts only native typed data. It never parses model text or
// tool JSON, and has no effect without a channel-installed turn carrier. Nil
// invalidates an earlier weather enrichment (for example after a failed fetch).
func SetWeatherCard(ctx context.Context, card *domain.WeatherCard) {
	if p, ok := ctx.Value(responsePresentationKey{}).(*ResponsePresentation); ok {
		snapshot := card.Clone()
		p.weatherMu.Lock()
		p.weather = snapshot
		p.weatherMu.Unlock()
	}
}

// WeatherCard returns an independent snapshot of the native weather payload.
func (p *ResponsePresentation) WeatherCard() *domain.WeatherCard {
	if p == nil {
		return nil
	}
	p.weatherMu.RLock()
	defer p.weatherMu.RUnlock()
	return p.weather.Clone()
}

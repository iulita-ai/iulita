package telegrambusiness

import (
	"context"
	"time"
	"unicode/utf8"

	"github.com/iulita-ai/iulita/internal/security"
)

// recordSources preserves only source-attributed transcription/local extraction,
// never new account facts. Original text/revision/date remain unchanged. A stale
// generation cannot write into a paused, edited, deleted or rebound peer.
func (g *ProfileGenerator) recordSources(ctx context.Context, scope Scope, settings GenerationSettings, descriptor string, processed ProcessedSource) error {
	if len(processed.Extractions) > 3 || processed.Omitted < 0 || processed.Omitted > 3000 {
		return ErrQuota
	}
	return g.repo.Update(ctx, scope.Key, func(st *State) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c := st.Connection
		p := st.Peers[scope.PeerID]
		if c.Key != scope.Key || c.OwnerID != scope.OwnerID || c.Epoch != settings.Epoch || c.RuntimeGeneration != settings.RuntimeGeneration || c.RestoreGeneration != settings.RestoreGeneration || !c.Enabled || c.Paused || c.NeedsRefresh || c.Quarantined || p == nil || p.Revision != settings.ContextRevision || !c.Policy.Consent || !p.Policy.Consent || !c.Policy.Enabled || !p.Policy.Enabled {
			return ErrStale
		}
		p.LastMediaOmitted = processed.Omitted
		for _, extraction := range processed.Extractions {
			if !c.Policy.MediaConsent || !p.Policy.MediaConsent || descriptor == "" || c.Policy.MediaProvider != descriptor || p.Policy.MediaProvider != descriptor {
				return ErrDenied
			}
			text := security.RedactSensitiveText(extraction.Text)
			if len(text) > 16384 || !utf8.ValidString(text) {
				return ErrQuota
			}
			found := false
			for index := range p.Messages {
				message := &p.Messages[index]
				if message.ID != extraction.MessageID {
					continue
				}
				if message.Deleted || message.Revision != extraction.Revision || len(message.Media) == 0 || message.Date.Before(time.Now().UTC().Add(-7*24*time.Hour)) {
					return ErrStale
				}
				found = true
				if message.ExtractedText != text || message.ExtractedProvider != descriptor {
					message.ExtractedText = text
					message.ExtractedProvider = descriptor
					p.Summary = nil
				}
			}
			if !found {
				return ErrStale
			}
		}
		return nil
	})
}

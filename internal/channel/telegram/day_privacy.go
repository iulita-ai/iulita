package telegram

import (
	"github.com/iulita-ai/iulita/internal/dayview"
	"github.com/iulita-ai/iulita/internal/security"
)

// Sanitize before both persistence and projection, not after truncation: a
// shortened credential can evade the detector. IDs/revisions remain authority.
func sanitizeDaySections(sections []dayview.Section) {
	for i := range sections {
		s := &sections[i]
		s.Name = security.RedactSensitiveText(s.Name)
		for j := range s.Items {
			it := &s.Items[j]
			it.Origin = truncateRunes(security.RedactSensitiveText(it.Origin), 160)
			it.Title = truncateRunes(security.RedactSensitiveText(it.Title), 512)
			it.Due = truncateRunes(security.RedactSensitiveText(it.Due), 128)
			it.Deadline = truncateRunes(security.RedactSensitiveText(it.Deadline), 128)
		}
	}
}

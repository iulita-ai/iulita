package dayview

import "time"

// PublicSnapshot contains an explicitly approved public export. Text is bounded
// and redacted before insertion; Owner is metadata and never exported to widgets.
type PublicSnapshot struct {
	ID        string `bun:",pk"`
	Owner     string
	Instance  string
	WidgetID  string
	Text      string
	CreatedAt time.Time
	ExpiresAt time.Time
	Withdrawn bool
}

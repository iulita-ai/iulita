// Package dayview defines the typed, owner-scoped projection used by the Telegram day card.
// Providers return data, never model-generated prose or arbitrary executable commands.
package dayview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// ErrUnavailable reports an unavailable or unauthorized source.
var ErrUnavailable = errors.New("source unavailable")

// ErrPartial reports a source result with incomplete coverage.
var ErrPartial = errors.New("source result is incomplete")

// ErrStale requires refreshing an item whose pinned revision changed.
var ErrStale = errors.New("item changed; refresh before confirming")

// Item pins an owned source resource and its available day-card actions.
type Item struct {
	Origin             string `json:"origin,omitempty"`
	Deadline           string `json:"deadline,omitempty"`
	ResolveDelivery    bool   `json:"resolve_delivery,omitempty"`
	ConnectionRevision string `json:"connection_revision,omitempty"`
	ListID             string `json:"list_id,omitempty"`
	DateOnly           bool   `json:"date_only,omitempty"`
	Connection         string `json:"connection,omitempty"`
	ID                 string `json:"id"`
	Source             string `json:"source"`
	Title              string `json:"title"`
	Due                string `json:"due,omitempty"`
	Revision           string `json:"revision"`
	Complete           bool   `json:"complete,omitempty"`
	Reschedule         bool   `json:"reschedule,omitempty"`
}

// Revision hashes ordered source values into an opaque revision token.
func Revision(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:24]
}

// Source reads and changes resources within an explicit owner scope.
type Source interface {
	Name() string
	List(context.Context, string, time.Time, *time.Location) ([]Item, error)
	// Change must revalidate owner and expected item revision before writing.
	Change(context.Context, string, Item, string, time.Time) error
}

// Section groups a source result with its availability status.
type Section struct {
	Name   string
	Items  []Item
	Status string
}

// Service coordinates configured owner-scoped day sources.
type Service struct{ Sources []Source }

func (s *Service) Read(ctx context.Context, owner string, now time.Time, loc *time.Location) []Section {
	if owner == "" {
		return nil
	}
	if loc == nil {
		loc = time.UTC
	}
	sections := make([]Section, len(s.Sources))
	var wg sync.WaitGroup
	for index, p := range s.Sources {
		wg.Add(1)
		go func(index int, p Source) {
			defer wg.Done()
			c, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			items, err := p.List(c, owner, now, loc)
			status := "ready"
			if err != nil {
				status = "unavailable"
				if !errors.Is(err, ErrPartial) {
					items = nil
				}
			}
			if errors.Is(err, ErrPartial) {
				status = "partial"
			}
			if errors.Is(err, ErrUnavailable) {
				status = "not_connected"
			}
			sections[index] = Section{p.Name(), items, status}
		}(index, p)
	}
	wg.Wait()
	return sections
}

// Change dispatches a change to the matching owned source.
func (s *Service) Change(ctx context.Context, owner string, item Item, action string, due time.Time) error {
	for _, p := range s.Sources {
		if p.Name() == item.Source {
			return p.Change(ctx, owner, item, action, due)
		}
	}
	return ErrUnavailable
}

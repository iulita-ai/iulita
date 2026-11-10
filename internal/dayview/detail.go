package dayview

import (
	"context"
	"time"
)

// Detail is a server-only owned view. Result text is never placed into widget
// public state; the transport must apply privacy and bounds before display.
type Detail struct {
	Title      string
	Status     string
	StartedAt  *time.Time
	FinishedAt *time.Time
	Summary    string
	Result     string
}

// DetailSource provides an owned detailed view without putting private results in public state.
type DetailSource interface {
	Detail(context.Context, string, Item) (Detail, error)
}

// Detail revalidates an owned item before returning server-side details.
func (s *Service) Detail(ctx context.Context, owner string, item Item) (Detail, error) {
	if owner == "" {
		return Detail{}, ErrUnavailable
	}
	for _, source := range s.Sources {
		if source.Name() != item.Source {
			continue
		}
		if details, ok := source.(DetailSource); ok {
			return details.Detail(ctx, owner, item)
		}
		items, err := source.List(ctx, owner, time.Now(), time.UTC)
		if err != nil {
			return Detail{}, err
		}
		for index := range items {
			current := &items[index]
			if current.ID == item.ID && current.Revision == item.Revision && current.Connection == item.Connection {
				return Detail{Title: current.Title, Summary: current.Due}, nil
			}
		}
		return Detail{}, ErrStale
	}
	return Detail{}, ErrUnavailable
}

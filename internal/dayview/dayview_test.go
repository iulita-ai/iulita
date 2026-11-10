package dayview

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fake struct {
	name string
	err  error
}

func (f fake) Name() string { return f.name }
func (f fake) List(context.Context, string, time.Time, *time.Location) ([]Item, error) {
	return []Item{{ID: "1", Source: f.name, Title: "Task"}}, f.err
}
func (f fake) Change(context.Context, string, Item, string, time.Time) error { return f.err }
func TestReadPartialSourcesAndNoOwner(t *testing.T) {
	s := Service{Sources: []Source{fake{"ok", nil}, fake{"off", ErrUnavailable}, fake{"failed", errors.New("upstream")}}}
	if len(s.Read(context.Background(), "", time.Now(), time.UTC)) != 0 {
		t.Fatal("unowned read")
	}
	sections := s.Read(context.Background(), "owner", time.Now(), time.UTC)
	if sections[0].Status != "ready" || len(sections[0].Items) != 1 || sections[1].Status != "not_connected" || sections[2].Status != "unavailable" || len(sections[2].Items) != 0 {
		t.Fatalf("%+v", sections)
	}
}
func TestRevisionBoundaries(t *testing.T) {
	if Revision("ab", "c") == Revision("a", "bc") {
		t.Fatal("ambiguous fingerprint")
	}
}

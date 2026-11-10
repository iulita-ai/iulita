package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/dayview"
)

func TestDayDeliveryRestartAndOperationDedup(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/day.db"
	s, e := New(path)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.EnsureDaySchema(ctx); e != nil {
		t.Fatal(e)
	}
	card := &dayview.Card{ID: "card", Instance: "a", Owner: "alice", Chat: 1, Message: 8, Revision: 2, DeliveredRevision: 1, DesiredState: `{"h":"My day"}`, Expires: time.Now().Add(time.Hour)}
	if e = s.SaveDayCard(ctx, card); e != nil {
		t.Fatal(e)
	}
	op := &dayview.Operation{ID: "op", CardID: card.ID, Owner: card.Owner, Status: "running", CreatedAt: time.Now()}
	ok, e := s.ClaimDayOperation(ctx, op)
	if e != nil || !ok {
		t.Fatal(e)
	}
	s.Close()
	s, e = New(path)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.EnsureDaySchema(ctx); e != nil {
		t.Fatal(e)
	}
	ok, e = s.ClaimDayOperation(ctx, op)
	if e != nil || ok {
		t.Fatal("operation replayed", e)
	}
	cards, e := s.PendingDayCards(ctx, "a")
	if e != nil || len(cards) != 1 || cards[0].DesiredState != card.DesiredState {
		t.Fatal(cards, e)
	}
	wrong, _ := s.PendingDayCards(ctx, "b")
	if len(wrong) != 0 {
		t.Fatal("cross instance delivery")
	}
	if e = s.MarkDayDelivered(ctx, "card", 1); e != nil {
		t.Fatal(e)
	}
	cards, _ = s.PendingDayCards(ctx, "a")
	if len(cards) != 1 {
		t.Fatal("stale ack lost new revision")
	}
	_ = s.MarkDayDelivered(ctx, "card", 2)
	cards, _ = s.PendingDayCards(ctx, "a")
	if len(cards) != 0 {
		t.Fatal("already delivered")
	}
}

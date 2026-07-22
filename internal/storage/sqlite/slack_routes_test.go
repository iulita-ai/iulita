package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
)

func TestSlackRoute_UpsertAndGet(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	const inst = "slack-personal"

	// Missing route returns (nil, nil) so callers can fall back.
	got, err := store.GetSlackRoute(ctx, inst, "slack:D404")
	if err != nil {
		t.Fatalf("GetSlackRoute miss: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for missing route, got %+v", got)
	}

	route := &domain.SlackChatRoute{
		ChatID:      "slack:C1:U2",
		InstanceID:  inst,
		ChannelID:   "C1",
		SlackUserID: "U2",
		ThreadTS:    "1700.0001",
		Locale:      "en",
	}
	if err = store.UpsertSlackRoute(ctx, route); err != nil {
		t.Fatalf("UpsertSlackRoute insert: %v", err)
	}

	// A different instance must NOT see this route (instance isolation).
	other, err := store.GetSlackRoute(ctx, "other-instance", "slack:C1:U2")
	if err != nil {
		t.Fatalf("GetSlackRoute other instance: %v", err)
	}
	if other != nil {
		t.Fatalf("expected instance isolation, got %+v", other)
	}

	got, err = store.GetSlackRoute(ctx, inst, "slack:C1:U2")
	if err != nil {
		t.Fatalf("GetSlackRoute: %v", err)
	}
	if got == nil {
		t.Fatal("expected route after insert")
	}
	if got.ChannelID != "C1" || got.SlackUserID != "U2" || got.ThreadTS != "1700.0001" || got.Locale != "en" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if got.UpdatedAt.IsZero() {
		t.Error("expected UpdatedAt to be set on upsert")
	}

	// Upsert on the same chatID updates in place (no duplicate row).
	route.ThreadTS = "1800.0002"
	route.Locale = "ru"
	if err = store.UpsertSlackRoute(ctx, route); err != nil {
		t.Fatalf("UpsertSlackRoute update: %v", err)
	}
	got, err = store.GetSlackRoute(ctx, inst, "slack:C1:U2")
	if err != nil {
		t.Fatalf("GetSlackRoute after update: %v", err)
	}
	if got.ThreadTS != "1800.0002" || got.Locale != "ru" {
		t.Errorf("update not applied: %+v", got)
	}
}

func TestSlackRoute_DeleteOlderThan(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	const inst = "slack-personal"

	if err := store.UpsertSlackRoute(ctx, &domain.SlackChatRoute{
		ChatID: "slack:C9:U9", InstanceID: inst, ChannelID: "C9", SlackUserID: "U9",
	}); err != nil {
		t.Fatalf("UpsertSlackRoute: %v", err)
	}
	now := time.Now()

	// A cutoff in the past keeps the fresh row.
	n, err := store.DeleteSlackRoutesOlderThan(ctx, inst, now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("DeleteSlackRoutesOlderThan (past): %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 deleted for past cutoff, got %d", n)
	}

	// A different instance must not delete this instance's rows.
	if n, err = store.DeleteSlackRoutesOlderThan(ctx, "other", now.Add(time.Hour)); err != nil {
		t.Fatalf("DeleteSlackRoutesOlderThan (other): %v", err)
	} else if n != 0 {
		t.Fatalf("expected 0 deleted for other instance, got %d", n)
	}

	// A cutoff in the future prunes the row.
	if n, err = store.DeleteSlackRoutesOlderThan(ctx, inst, now.Add(time.Hour)); err != nil {
		t.Fatalf("DeleteSlackRoutesOlderThan (future): %v", err)
	} else if n != 1 {
		t.Fatalf("expected 1 deleted for future cutoff, got %d", n)
	}

	got, err := store.GetSlackRoute(ctx, inst, "slack:C9:U9")
	if err != nil {
		t.Fatalf("GetSlackRoute after prune: %v", err)
	}
	if got != nil {
		t.Fatalf("expected route pruned, got %+v", got)
	}
}

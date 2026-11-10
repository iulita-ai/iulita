package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
)

type pauseFixtureSender struct {
	err   error
	calls int
}

func (s *pauseFixtureSender) SendMessage(context.Context, string, string) error {
	s.calls++
	return s.err
}
func TestKnownPausedReminderIsSuppressedNotUnknownAndNeverReplayed(t *testing.T) {
	ctx := context.Background()
	store, e := sqlite.New(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	if e = store.RunMigrations(ctx); e != nil {
		t.Fatal(e)
	}
	due := time.Now().Add(-time.Minute).Truncate(time.Second)
	r := &domain.Reminder{ChatID: "42", UserID: "alice", Title: "Reminder", DueAt: due, Timezone: "UTC", Status: "pending"}
	if e = store.CreateReminder(ctx, r); e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(reminderPayload{ReminderID: r.ID, ChatID: r.ChatID, Title: r.Title, DueAt: due.Format(time.RFC3339), Timezone: r.Timezone})
	sender := &pauseFixtureSender{err: fmt.Errorf("topic gate: %w", channel.ErrDeliveryPaused)}
	h := NewReminderFireHandler(store, sender)
	result, e := h.Handle(ctx, string(raw))
	if e != nil || result != `{"status":"suppressed","reason":"topic_paused"}` {
		t.Fatal(result, e)
	}
	source := sqlite.ReminderDaySource{Store: store, Chat: r.ChatID}
	items, e := source.List(ctx, "alice", time.Now(), time.UTC)
	if e != nil || len(items) != 1 || !items[0].Reschedule || items[0].ResolveDelivery {
		t.Fatal("suppressed delivery was treated as unknown", items, e)
	}
	// Resuming transport does not requeue a known suppressed reminder.
	sender.err = nil
	if _, e = h.Handle(ctx, string(raw)); e != nil || sender.calls != 1 {
		t.Fatal("resume replayed a suppressed delivery", e)
	}
	future := time.Now().Add(time.Hour)
	if e = source.Change(ctx, "bob", items[0], "reschedule", future); e == nil {
		t.Fatal("foreign actor rescheduled")
	}
	if e = source.Change(ctx, "alice", items[0], "reschedule", future); e != nil {
		t.Fatal(e)
	}
	if _, e = h.Handle(ctx, string(raw)); e != nil || sender.calls != 1 {
		t.Fatal("old queued payload replayed after explicit new time", e)
	}
	pending, e := store.GetDueReminders(ctx, time.Now())
	if e != nil || len(pending) != 0 {
		t.Fatal("future reminder queued immediately")
	}
}
func TestNetworkFailureReminderRemainsUnknown(t *testing.T) {
	ctx := context.Background()
	store, e := sqlite.New(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	if e = store.RunMigrations(ctx); e != nil {
		t.Fatal(e)
	}
	due := time.Now().Add(-time.Minute).Truncate(time.Second)
	r := &domain.Reminder{ChatID: "42", UserID: "alice", Title: "Reminder", DueAt: due, Timezone: "UTC", Status: "pending"}
	if e = store.CreateReminder(ctx, r); e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(reminderPayload{ReminderID: r.ID, ChatID: r.ChatID, DueAt: due.Format(time.RFC3339), Timezone: r.Timezone})
	h := NewReminderFireHandler(store, &pauseFixtureSender{err: errors.New("network timeout")})
	if _, e = h.Handle(ctx, string(raw)); e == nil {
		t.Fatal("network failure hidden")
	}
	items, e := (sqlite.ReminderDaySource{Store: store, Chat: r.ChatID}).List(ctx, "alice", time.Now(), time.UTC)
	if e != nil || len(items) != 1 || !items[0].ResolveDelivery || items[0].Reschedule {
		t.Fatal("uncertain network error became definite paused", items, e)
	}
}

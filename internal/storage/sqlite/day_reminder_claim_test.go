package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
)

func TestReminderClaimExcludesSnoozeAndDuplicateSend(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	due := time.Now().Add(-time.Minute).Truncate(time.Second)
	r := &domain.Reminder{ChatID: "42", UserID: "owner", Title: "reminder", DueAt: due, Timezone: "UTC", Status: "pending"}
	if e := s.CreateReminder(ctx, r); e != nil {
		t.Fatal(e)
	}
	source := ReminderDaySource{Store: s, Chat: "42"}
	items, e := source.List(ctx, "owner", time.Now(), time.UTC)
	if e != nil || len(items) != 1 {
		t.Fatal(e)
	}
	claimed, e := s.ClaimReminderDelivery(ctx, r.ID, "42", due)
	if e != nil || claimed == nil {
		t.Fatalf("claim: %v %v", claimed, e)
	}
	if e = source.Change(ctx, "owner", items[0], "reschedule", time.Now().Add(time.Hour)); e == nil {
		t.Fatal("snooze raced past delivery claim")
	}
	if e = s.DeleteReminder(ctx, r.ID, "42"); e == nil {
		t.Fatal("cancel raced past delivery claim")
	}
	if second, checkErr := s.ClaimReminderDelivery(ctx, r.ID, "42", due); checkErr != nil || second != nil {
		t.Fatal("duplicate delivery claim")
	}
	if e = s.MarkReminderDeliveryUnknown(ctx, r.ID); e != nil {
		t.Fatal(e)
	}
	visible, e := source.List(ctx, "owner", time.Now(), time.UTC)
	if e != nil || len(visible) != 1 || visible[0].Reschedule {
		t.Fatal("uncertain delivery hidden or mutable")
	}
}
func TestRescheduleInvalidatesQueuedPayloadAndAllowsNewDue(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	due := time.Now().Add(-time.Minute).Truncate(time.Second)
	r := &domain.Reminder{ChatID: "42", UserID: "owner", Title: "reminder", DueAt: due, Timezone: "UTC", Status: "pending"}
	if e := s.CreateReminder(ctx, r); e != nil {
		t.Fatal(e)
	}
	source := ReminderDaySource{Store: s, Chat: "42"}
	items, e := source.List(ctx, "owner", time.Now(), time.UTC)
	if e != nil {
		t.Fatal(e)
	}
	if e = source.Change(ctx, "owner", items[0], "reschedule", time.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	if old, e := s.ClaimReminderDelivery(ctx, r.ID, "42", due); e != nil || old != nil {
		t.Fatal("old payload delivered after snooze")
	}
	if got, e := s.GetDueReminders(ctx, time.Now()); e != nil || len(got) != 0 {
		t.Fatal("future reminder became due")
	}
}

func TestCreateDayReminderAtomicAndOwnerBound(t *testing.T) {
	s := newTestStore(t)
	if e := s.EnsureDaySchema(context.Background()); e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	route := &domain.TelegramConversationRoute{ConversationID: "tgconv:owned", InstanceID: "testbot", ChatID: 42, OwnerID: "alice", Private: true}
	if e := s.PutTelegramRoute(ctx, route); e != nil {
		t.Fatal(e)
	}
	due := time.Now().Add(time.Hour).Truncate(time.Second)
	id, created, e := s.CreateDayReminder(ctx, "alice", route.ConversationID, "remember card 4111111111111111", "UTC", due, "op-one")
	if e != nil || !created || id == 0 {
		t.Fatalf("create: %d %v %v", id, created, e)
	}
	same, created, e := s.CreateDayReminder(ctx, "alice", route.ConversationID, "remember card 4111111111111111", "UTC", due, "op-one")
	if e != nil || created || same != id {
		t.Fatalf("duplicate create: %d %v %v", same, created, e)
	}
	if _, _, e = s.CreateDayReminder(ctx, "bob", route.ConversationID, "stolen", "UTC", due, "op-two"); e == nil {
		t.Fatal("foreign route accepted")
	}
	if _, _, e = s.CreateDayReminder(ctx, "alice", route.ConversationID, "different command", "UTC", due, "op-one"); e == nil {
		t.Fatal("idempotency key rebound to another command")
	}
	reminders, e := s.ListReminders(ctx, route.ConversationID)
	if e != nil || len(reminders) != 1 {
		t.Fatal("duplicate reminder")
	}
	if reminders[0].Title == "remember card 4111111111111111" {
		t.Fatal("raw payment stored")
	}
}

func TestUncertainReminderResolutionIsOwnerBoundAndNeverResends(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if e := s.EnsureDaySchema(ctx); e != nil {
		t.Fatal(e)
	}
	due := time.Now().Add(-time.Hour).Truncate(time.Second)
	r := &domain.Reminder{ChatID: "42", UserID: "alice", Title: "reminder", DueAt: due, Timezone: "UTC", Status: "pending"}
	if e := s.CreateReminder(ctx, r); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ClaimReminderDelivery(ctx, r.ID, "42", due); e != nil {
		t.Fatal(e)
	}
	if _, e := s.db.NewUpdate().Model((*reminderDeliveryClaim)(nil)).Set("claimed_at = ?", time.Now().Add(-time.Hour)).Where("reminder_id = ?", r.ID).Exec(ctx); e != nil {
		t.Fatal(e)
	}
	if e := s.ReconcileReminderClaims(ctx, time.Now()); e != nil {
		t.Fatal(e)
	}
	if e := s.ResolveReminderDelivery(ctx, "bob", "42", r.ID, "delivery_unknown", "acknowledged"); e == nil {
		t.Fatal("foreign actor resolved")
	}
	if e := s.ResolveReminderDelivery(ctx, "alice", "42", r.ID, "delivery_unknown", "acknowledged"); e != nil {
		t.Fatal(e)
	}
	if e := s.ResolveReminderDelivery(ctx, "alice", "42", r.ID, "delivery_unknown", "acknowledged"); e == nil {
		t.Fatal("resolution replay accepted")
	}
	dueItems, e := s.GetDueReminders(ctx, time.Now())
	if e != nil || len(dueItems) != 0 {
		t.Fatal("resolution replayed delivery")
	}
}

package conversations

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNativeReminderAtomicReplayRestartOfflineDeliveryAndClear(t *testing.T) {
	r, path, owner, other, key := fixture(t)
	c := create(t, r, owner)
	_, _, err := r.Accept(ctx, owner, c.ID, input("schedule"))
	if err != nil {
		t.Fatal(err)
	}
	rt, err := r.AcquireRuntime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer r.ReleaseRuntime(ctx, rt)
	_, f, err := r.Claim(ctx, rt, owner)
	if err != nil {
		t.Fatal(err)
	}
	a := ApprovalAuthority{OwnerID: owner, DeviceID: uuid.NewString(), FamilyID: uuid.NewString(), Scopes: []string{"chats:write"}}
	invoke := func(id string, in ReminderInput) (string, error) {
		payload, _ := json.Marshal(in)
		if _, prepareErr := r.PrepareTool(ctx, f, id, "mutating", payload); prepareErr != nil {
			return "", prepareErr
		}
		if startErr := r.StartTool(ctx, f, id); startErr != nil {
			return "", startErr
		}
		return r.Reminders(ctx, f, id, in, a)
	}
	in := ReminderInput{Action: "create", Title: "OFFLINE_REMINDER_CANARY", DueAt: time.Now().Add(time.Hour).Format(time.RFC3339)}
	out, err := invoke("schedule", in)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := r.Reminders(ctx, f, "schedule", in, a)
	if err != nil || replay != out {
		t.Fatal("atomic result replay", replay, err)
	}
	var result struct {
		ID int64 `json:"id"`
	}
	if err = json.Unmarshal([]byte(out), &result); err != nil || result.ID == 0 {
		t.Fatal(out, err)
	}
	foreign := f
	foreign.OwnerID = other
	if _, err = r.Reminders(ctx, foreign, "schedule", in, a); err == nil {
		t.Fatal("foreign reminder access")
	}
	if err = r.FinishTool(ctx, f, "schedule", "succeeded", out); err != nil {
		t.Fatal(err)
	}
	if err = r.Complete(ctx, f, "scheduled", "completed"); err != nil {
		t.Fatal(err)
	}
	if _, err = r.db.Exec(`UPDATE mobile_reminders SET due_at=? WHERE id=?`, time.Now().Add(-time.Second).UnixMilli(), result.ID); err != nil {
		t.Fatal(err)
	}
	restarted, err := OpenExisting(ctx, path, key)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	denied := func(context.Context, ApprovalAuthority) error { return ErrApproval }
	if delivered, deliveryErr := restarted.DeliverReminder(ctx, result.ID, denied); deliveryErr != nil || delivered {
		t.Fatal("revoked family delivered", deliveryErr)
	}
	verify := func(_ context.Context, got ApprovalAuthority) error {
		if got.OwnerID != owner || got.FamilyID != a.FamilyID {
			return ErrApproval
		}
		return nil
	}
	for n := 0; n < 2; n++ {
		delivered, deliveryErr := restarted.DeliverReminder(ctx, result.ID, verify)
		if deliveryErr != nil || delivered != (n == 0) {
			t.Fatal("delivery/replay", n, delivered, deliveryErr)
		}
	}
	messages, _, err := r.Messages(ctx, owner, c.ID, 1, 20, "")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, m := range messages {
		if strings.Contains(m.Text, "Напоминание: OFFLINE") {
			count++
		}
	}
	if count != 1 {
		t.Fatal("offline transcript delivery", count)
	}
	// A generation reset cannot resurrect an outstanding reminder.
	if _, err = r.db.Exec(`INSERT INTO mobile_reminders(owner_id,conversation_id,generation,origin_turn,operation_id,title,due_at,state,authority,delivery_id) VALUES(?,?,1,?,'clear','ERASE_ME',?,'pending',?,?)`, owner, c.ID, f.TurnID, time.Now().Add(-time.Second).UnixMilli(), `{}`, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Clear(ctx, owner, c.ID, 1); err != nil {
		t.Fatal(err)
	}
	ids, err := restarted.DueReminders(ctx)
	if err != nil || len(ids) != 0 {
		t.Fatal("cleared reminders runnable", ids, err)
	}
	var raw string
	if err = r.db.QueryRow(`SELECT title || authority FROM mobile_reminders WHERE operation_id='clear'`).Scan(&raw); err != nil || strings.Contains(raw, "ERASE_ME") {
		t.Fatal("clear retained private reminder", err)
	}
}

func TestReminderCancellationOwnerScopeAndFailedAtomicWrite(t *testing.T) {
	r, _, owner, other, _ := fixture(t)
	c := create(t, r, owner)
	_, _, _ = r.Accept(ctx, owner, c.ID, input("cancel"))
	rt, _ := r.AcquireRuntime(ctx)
	defer r.ReleaseRuntime(ctx, rt)
	_, f, err := r.Claim(ctx, rt, owner)
	if err != nil {
		t.Fatal(err)
	}
	a := ApprovalAuthority{OwnerID: owner, DeviceID: uuid.NewString(), FamilyID: uuid.NewString()}
	for _, id := range []string{"invalid", "cancel"} {
		if _, err = r.PrepareTool(ctx, f, id, "mutating", []byte(id)); err != nil {
			t.Fatal(err)
		}
		if err = r.StartTool(ctx, f, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = r.Reminders(ctx, f, "invalid", ReminderInput{Action: "create", Title: "late", DueAt: time.Now().Add(-time.Hour).Format(time.RFC3339)}, a); !errors.Is(err, ErrInvalid) {
		t.Fatal("past reminder", err)
	}
	otherChat := create(t, r, other)
	raw, _ := json.Marshal(a)
	var id int64
	if err = r.db.QueryRow(`INSERT INTO mobile_reminders(owner_id,conversation_id,generation,origin_turn,operation_id,title,due_at,state,authority,delivery_id) VALUES(?,?,1,?,'foreign','private',?,'pending',?,?) RETURNING id`, other, otherChat.ID, f.TurnID, time.Now().Add(time.Hour).UnixMilli(), raw, uuid.NewString()).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Reminders(ctx, f, "cancel", ReminderInput{Action: "delete", ID: id}, a); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign cancellation", err)
	}
	var count int
	if err = r.db.QueryRow(`SELECT COUNT(*) FROM mobile_reminders WHERE owner_id=? AND state='pending'`, other).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if _, err = r.db.Exec(`UPDATE mobile_reminders SET owner_id=?,conversation_id=? WHERE id=?`, owner, c.ID, id); err != nil {
		t.Fatal(err)
	}
	out, err := r.Reminders(ctx, f, "cancel", ReminderInput{Action: "delete", ID: id}, a)
	if err != nil || !strings.Contains(out, "cancelled") {
		t.Fatal(out, err)
	}
}

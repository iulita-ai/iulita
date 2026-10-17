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

func reviewReminderOrigin(t *testing.T, r *Repository, rt Runtime, owner string) (Conversation, Fence, ApprovalAuthority) {
	t.Helper()
	c := create(t, r, owner)
	if _, _, err := r.Accept(ctx, owner, c.ID, input(uuid.NewString())); err != nil {
		t.Fatal(err)
	}
	_, f, err := r.Claim(ctx, rt, owner)
	if err != nil {
		t.Fatal(err)
	}
	return c, f, ApprovalAuthority{OwnerID: owner, DeviceID: uuid.NewString(), FamilyID: uuid.NewString(), Scopes: []string{"chats:write"}}
}
func reviewInsertReminder(t *testing.T, r *Repository, c Conversation, f Fence, a ApprovalAuthority) int64 {
	t.Helper()
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	err = r.db.QueryRow(`INSERT INTO mobile_reminders(owner_id,conversation_id,generation,origin_turn,operation_id,title,due_at,state,authority,delivery_id) VALUES(?,?,1,?,?,'KEEP_PRIVATE_INTENT',?,'pending',?,?) RETURNING id`, f.OwnerID, c.ID, f.TurnID, uuid.NewString(), time.Now().Add(-time.Minute).UnixMilli(), raw, uuid.NewString()).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func TestReminderQueueBackoffLetsOtherOwnerDeliver(t *testing.T) {
	r, _, owner, other, _ := fixture(t)
	rt, err := r.AcquireRuntime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer r.ReleaseRuntime(ctx, rt)
	c, f, a := reviewReminderOrigin(t, r, rt, owner)
	// Fill the owner's transcript without constructing a second user-visible turn.
	if _, err = r.db.Exec(`UPDATE mobile_messages SET text=CAST(zeroblob(?) AS TEXT) WHERE owner_id=?`, 64<<20, owner); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 100; n++ {
		reviewInsertReminder(t, r, c, f, a)
	}
	c2, f2, a2 := reviewReminderOrigin(t, r, rt, other)
	wanted := reviewInsertReminder(t, r, c2, f2, a2)
	ids, err := r.DueReminders(ctx)
	if err != nil || len(ids) != 100 {
		t.Fatal(len(ids), err)
	}
	for _, id := range ids {
		if ok, deliveryErr := r.DeliverReminder(ctx, id, func(_ context.Context, _ ApprovalAuthority) error { return nil }); ok || !errors.Is(deliveryErr, ErrQueueFull) {
			t.Fatal(ok, deliveryErr)
		}
	}
	ids, err = r.DueReminders(ctx)
	if err != nil || len(ids) != 1 || ids[0] != wanted {
		t.Fatal("starved other owner", ids, err)
	}
	if ok, deliveryErr := r.DeliverReminder(ctx, wanted, func(_ context.Context, _ ApprovalAuthority) error { return nil }); !ok || deliveryErr != nil {
		t.Fatal(ok, deliveryErr)
	}
	var count int
	if err = r.db.QueryRow(`SELECT count(*) FROM mobile_reminders WHERE owner_id=? AND state='pending' AND title='KEEP_PRIVATE_INTENT' AND retry_at>?`, owner, time.Now().UnixMilli()).Scan(&count); err != nil || count != 100 {
		t.Fatal("failed intent erased", count, err)
	}
}
func TestReminderTransientStampFailurePreservesIntentAndRetries(t *testing.T) {
	r, _, owner, _, _ := fixture(t)
	rt, err := r.AcquireRuntime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer r.ReleaseRuntime(ctx, rt)
	c, f, a := reviewReminderOrigin(t, r, rt, owner)
	id := reviewInsertReminder(t, r, c, f, a)
	// Operational schema/storage failure, not a missing/disabled account.
	if _, err = r.db.Exec(`ALTER TABLE users RENAME COLUMN password_hash TO unavailable_password_hash`); err != nil {
		t.Fatal(err)
	}
	verifyCalls := 0
	verify := func(_ context.Context, _ ApprovalAuthority) error { verifyCalls++; return nil }
	if ok, deliveryErr := r.DeliverReminder(ctx, id, verify); ok || deliveryErr == nil {
		t.Fatal(ok, deliveryErr)
	}
	var state, title string
	if err = r.db.QueryRow(`SELECT state,title FROM mobile_reminders WHERE id=?`, id).Scan(&state, &title); err != nil || state != "pending" || title != "KEEP_PRIVATE_INTENT" || verifyCalls != 0 {
		t.Fatal(state, title, verifyCalls, err)
	}
	if _, err = r.db.Exec(`ALTER TABLE users RENAME COLUMN unavailable_password_hash TO password_hash`); err != nil {
		t.Fatal(err)
	}
	if ok, deliveryErr := r.DeliverReminder(ctx, id, verify); !ok || deliveryErr != nil {
		t.Fatal(ok, deliveryErr)
	}
}
func TestReminderListContinuationAfterTenAndOwnerIsolation(t *testing.T) {
	r, _, owner, other, _ := fixture(t)
	rt, err := r.AcquireRuntime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer r.ReleaseRuntime(ctx, rt)
	c, f, a := reviewReminderOrigin(t, r, rt, owner)
	var last int64
	for n := 0; n < 11; n++ {
		last = reviewInsertReminder(t, r, c, f, a)
	}
	c2, f2, a2 := reviewReminderOrigin(t, r, rt, other)
	reviewInsertReminder(t, r, c2, f2, a2)
	list := func(op string, after int64) string {
		if _, prepareErr := r.PrepareTool(ctx, f, op, "read", []byte(op)); prepareErr != nil {
			t.Fatal(prepareErr)
		}
		if startErr := r.StartTool(ctx, f, op); startErr != nil {
			t.Fatal(startErr)
		}
		out, listErr := r.Reminders(ctx, f, op, ReminderInput{Action: "list", AfterID: after}, a)
		if listErr != nil {
			t.Fatal(listErr)
		}
		return out
	}
	var page struct {
		Reminders []Reminder `json:"reminders"`
		More      bool       `json:"more"`
		Next      int64      `json:"next_after_id"`
	}
	if err = json.Unmarshal([]byte(list("first", 0)), &page); err != nil || len(page.Reminders) != 10 || !page.More || page.Next == 0 {
		t.Fatal(page, err)
	}
	out := list("next", page.Next)
	page.Reminders = nil
	page.More = false
	page.Next = 0
	if err = json.Unmarshal([]byte(out), &page); err != nil || len(page.Reminders) != 1 || page.Reminders[0].ID != last || page.More || strings.Contains(out, c2.ID) {
		t.Fatal(out, err)
	}
}

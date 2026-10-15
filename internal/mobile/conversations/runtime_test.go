package conversations

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestFencesContextAndUnknownEffect(t *testing.T) {
	r, _, owner, _, _ := fixture(t)
	c := create(t, r, owner)
	rt, err := r.AcquireRuntime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.AcquireRuntime(ctx); !errors.Is(err, ErrBusy) {
		t.Fatal("second live runtime admitted", err)
	}
	a, _, _ := r.Accept(ctx, owner, c.ID, input("A"))
	b, _, _ := r.Accept(ctx, owner, c.ID, input("B"))
	_, _, _ = r.Accept(ctx, owner, c.ID, input("C"))
	turn, f, err := r.Claim(ctx, rt, owner)
	if err != nil || turn.ID != a.ID {
		t.Fatal("claim", err)
	}
	history, text, err := r.ContextFor(ctx, f)
	if err != nil || len(history) != 0 || text != "A" {
		t.Fatal("future inputs entered A", history, text, err)
	}
	if err = r.Complete(ctx, f, "answer A", "completed"); err != nil {
		t.Fatal(err)
	}
	turn, f, err = r.Claim(ctx, rt, owner)
	if err != nil || turn.ID != b.ID {
		t.Fatal(err)
	}
	history, text, err = r.ContextFor(ctx, f)
	if err != nil || text != "B" || len(history) != 2 || history[1].Text != "answer A" {
		t.Fatal("B context", history, text, err)
	}
	business, cancel := r.TurnContext(context.Background(), f)
	defer cancel()
	op, err := r.PrepareTool(ctx, f, "mutation-1", "mutating", []byte("payload"))
	if err != nil || op.State != "prepared" {
		t.Fatal(err)
	}
	if err = r.StartTool(ctx, f, op.ID); err != nil {
		t.Fatal(err)
	}
	canceled, err := r.Cancel(ctx, owner, c.ID, b.ID, 1)
	if err != nil || canceled.State != "unknown_outcome" || !canceled.CancellationRequested {
		t.Fatal("cancel claimed rollback", canceled, err)
	}
	if err = r.Complete(ctx, f, "late reply", "completed"); !errors.Is(err, ErrFenced) {
		t.Fatal("late reply committed", err)
	}
	if err = r.FinishTool(ctx, f, op.ID, "succeeded", "late result"); !errors.Is(err, ErrFenced) {
		t.Fatal("late outcome committed", err)
	}
	select {
	case <-business.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("business cancellation detached")
	}
	old, _ := r.GetTurn(ctx, owner, c.ID, a.ID)
	old, err = r.Cancel(ctx, owner, c.ID, old.ID, 1)
	if err != nil || old.State != "completed" {
		t.Fatal("late cancel rewrote completed", err)
	}
}
func TestRuntimeRestartDoesNotReplayStartedWork(t *testing.T) {
	r, path, owner, _, key := fixture(t)
	c := create(t, r, owner)
	_, _, _ = r.Accept(ctx, owner, c.ID, input("once"))
	rt, _ := r.AcquireRuntime(ctx)
	turn, f, err := r.Claim(ctx, rt, owner)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.PrepareTool(ctx, f, "effect", "unknown", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if err = r.StartTool(ctx, f, "effect"); err != nil {
		t.Fatal(err)
	}
	if err = r.ReleaseRuntime(ctx, rt); err != nil {
		t.Fatal(err)
	}
	second, err := OpenExisting(ctx, path, key)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	newRT, err := second.AcquireRuntime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, err := second.GetTurn(ctx, owner, c.ID, turn.ID)
	if err != nil || got.State != "unknown_outcome" {
		t.Fatal("restart replay risk", got, err)
	}
	if _, _, err = second.Claim(ctx, newRT, owner); !errors.Is(err, ErrNoTurn) {
		t.Fatal("restarted LLM loop", err)
	}
	if err = r.CheckFence(ctx, f); !errors.Is(err, ErrFenced) {
		t.Fatal("old runtime still owns turn")
	}
}
func TestAtomicOperationAndPromptCAS(t *testing.T) {
	r, _, owner, other, _ := fixture(t)
	c := create(t, r, owner)
	opID := uuid.NewString()
	calls := 0
	fn := func(ctx context.Context) ([]byte, error) {
		calls++
		title := "renamed"
		_, err := r.Update(ctx, owner, c.ID, 1, &title, nil)
		return []byte("response"), err
	}
	for i := 0; i < 2; i++ {
		out, replay, err := r.Operation(ctx, owner, "client", "rename", opID, []byte("same"), fn)
		if err != nil || string(out) != "response" || replay != (i == 1) {
			t.Fatal("operation", err)
		}
	}
	if calls != 1 {
		t.Fatal("operation replayed")
	}
	if _, _, err := r.Operation(ctx, owner, "client", "rename", opID, []byte("changed"), fn); !errors.Is(err, ErrIdempotency) {
		t.Fatal("changed operation accepted")
	}
	_, _, _ = r.Accept(ctx, owner, c.ID, input("question"))
	rt, _ := r.AcquireRuntime(ctx)
	turn, f, _ := r.Claim(ctx, rt, owner)
	fp := sha256.Sum256([]byte("bound operation"))
	p, err := r.OpenPrompt(ctx, f, Prompt{Kind: "confirmation", OperationID: uuid.NewString(), Fingerprint: fp[:], Preview: "safe preview", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	device := uuid.NewString()
	if _, err = r.Decide(ctx, other, device, c.ID, turn.ID, p.ID, 1, "approve", p.OperationID, p.Nonce, fp[:]); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign prompt", err)
	}
	if _, err = r.Decide(ctx, owner, device, c.ID, turn.ID, p.ID, 1, "да", p.OperationID, p.Nonce, fp[:]); !errors.Is(err, ErrInvalid) {
		t.Fatal("free text confirmed")
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = r.Decide(ctx, owner, device, c.ID, turn.ID, p.ID, 1, "approve", p.OperationID, p.Nonce, fp[:])
		}(i)
	}
	wg.Wait()
	wins := 0
	for _, e := range errs {
		if e == nil {
			wins++
		} else if !errors.Is(e, ErrConflict) {
			t.Fatal(e)
		}
	}
	if wins != 1 {
		t.Fatal("prompt double consume")
	}
	if _, err = r.Clear(ctx, owner, c.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err = r.CheckFence(ctx, f); !errors.Is(err, ErrFenced) {
		t.Fatal("clear did not fence")
	}
}
func TestSnapshotMaterializationAndEventGap(t *testing.T) {
	r, _, owner, other, _ := fixture(t)
	first := create(t, r, owner)
	_ = create(t, r, owner)
	snap, err := r.Snapshot(ctx, owner, nil, 1, "")
	if err != nil || snap.NextPage == "" {
		t.Fatal("snapshot", err)
	}
	_, _, _ = r.Accept(ctx, owner, first.ID, input("after snapshot"))
	next, err := r.Snapshot(ctx, owner, nil, 1, snap.NextPage)
	if err != nil || next.ID != snap.ID || next.Watermark != snap.Watermark || len(next.Items) != 1 {
		t.Fatal("torn snapshot", next, err)
	}
	events, _, err := r.Events(ctx, owner, nil, snap.ID, snap.Cursor, 100)
	if err != nil || len(events) != 1 || events[0].Seq <= snap.Watermark {
		t.Fatal("snapshot-to-stream gap", events, err)
	}
	if _, _, err = r.Events(ctx, other, nil, snap.ID, snap.Cursor, 10); !errors.Is(err, ErrResync) {
		t.Fatal("foreign snapshot accepted")
	}
	if _, err = r.Clear(ctx, owner, first.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, _, err = r.Events(ctx, owner, nil, snap.ID, snap.Cursor, 10); !errors.Is(err, ErrResync) {
		t.Fatal("clear allowed stale snapshot")
	}
}

func TestClearErasesKnowledgePromptAndOperationBodiesButKeepsDedup(t *testing.T) {
	r, _, owner, _, _ := fixture(t)
	c := create(t, r, owner)
	const canary = "CLEARED_PRIVATE_KNOWLEDGE"
	turn, _, err := r.Admit(ctx, owner, c.ID, input("hello"), func(Turn) ([]byte, error) { return []byte(`{"SavedMemory":"` + canary + `"}`), nil })
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
	_, err = r.OpenPrompt(ctx, f, Prompt{Kind: "choice", Question: canary, Choices: []Choice{{ID: "yes", Label: canary}}, ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	op := uuid.NewString()
	calls := 0
	fn := func(context.Context) ([]byte, error) { calls++; return []byte(canary), nil }
	if _, _, err = r.Operation(ctx, owner, "client", "RespondToPrompt", op, []byte("payload"), fn, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Clear(ctx, owner, c.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, _, err = r.Operation(ctx, owner, "client", "RespondToPrompt", op, []byte("payload"), fn, c.ID); !errors.Is(err, ErrResync) {
		t.Fatal("cleared response replay", err)
	}
	if calls != 1 {
		t.Fatal("redacted operation replayed effect")
	}
	var policy string
	if err = r.db.QueryRow(`SELECT policy_snapshot FROM mobile_turns WHERE id=?`, turn.ID).Scan(&policy); err != nil || strings.Contains(policy, canary) {
		t.Fatal("knowledge survived clear", err)
	}
	var body string
	if err = r.db.QueryRow(`SELECT body FROM mobile_prompts WHERE turn_id=?`, turn.ID).Scan(&body); err != nil || strings.Contains(body, canary) {
		t.Fatal("prompt body survived clear", err)
	}
}

func TestTaskMetadataOutboxInboxSnapshotAndLostSourceACK(t *testing.T) {
	r, _, owner, _, _ := fixture(t)
	c := create(t, r, owner)
	turn, _, err := r.Accept(ctx, owner, c.ID, input("phone"))
	if err != nil {
		t.Fatal(err)
	}
	e := TaskEvent{ID: uuid.NewString(), Owner: owner, Conversation: c.ID, Turn: turn.ID, Task: uuid.NewString(), State: "running", Generation: 1, SourceSeq: 1, CreatedAt: time.Now()}
	for i := 0; i < 2; i++ {
		if err = r.PublishTask(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err = r.db.QueryRow(`SELECT COUNT(*) FROM mobile_events WHERE event_type='task.changed'`).Scan(&n); err != nil || n != 1 {
		t.Fatal("lost ACK duplicated durable event", n, err)
	}
	snap, err := r.Snapshot(ctx, owner, nil, 100, "")
	if err != nil || len(snap.Items) != 1 || len(snap.Items[0].TaskIDs) != 1 || snap.Items[0].TaskIDs[0] != e.Task {
		t.Fatal("task reference missing from snapshot", snap, err)
	}
	newer := e
	newer.ID = uuid.NewString()
	newer.State = "succeeded"
	newer.SourceSeq = 2
	if err = r.PublishTask(ctx, newer); err != nil {
		t.Fatal(err)
	}
	stale := e
	stale.ID = uuid.NewString()
	if err = r.PublishTask(ctx, stale); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = r.db.QueryRow(`SELECT state FROM mobile_task_references WHERE task_id=?`, e.Task).Scan(&state); err != nil || state != "succeeded" {
		t.Fatal("stale outbox delivery rolled back state", state, err)
	}
}

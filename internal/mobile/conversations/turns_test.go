package conversations

import (
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func input(text string) Input {
	return Input{ClientMessageID: uuid.NewString(), Generation: 1, Text: text}
}
func TestVersionOneMigrationRetainsTranscriptAndSequence(t *testing.T) {
	r, path, owner, _, key := fixture(t)
	c := create(t, r, owner)
	anchor := uuid.NewString()
	if err := r.BindTurn(ctx, owner, c.ID, 1, anchor, 7); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Append(ctx, owner, c.ID, 1, anchor, 7, "user", "existing transcript"); err != nil {
		t.Fatal(err)
	}
	// Reconstruct the exact BE04 v1 schema; OpenExisting must migrate data.
	for _, q := range []string{`DROP TABLE mobile_task_references`, `DROP TABLE mobile_task_event_inbox`, `DROP TABLE mobile_snapshots`, `DROP TABLE mobile_prompts`, `DROP TABLE mobile_tool_operations`, `DROP TABLE mobile_operations`, `DROP TABLE mobile_runtime`, `DROP TABLE mobile_events`, `DROP TABLE mobile_event_sequences`, `DROP TABLE mobile_turns`, `ALTER TABLE mobile_conversations DROP COLUMN next_turn_seq`, `DROP TABLE mobile_chat_schema`, `CREATE TABLE mobile_chat_schema(version INTEGER PRIMARY KEY CHECK(version=1))`, `INSERT INTO mobile_chat_schema VALUES(1)`} {
		if _, err := r.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	r.Close()
	reopened, err := OpenExisting(ctx, path, key)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	turn, _, err := reopened.Accept(ctx, owner, c.ID, input("new"))
	if err != nil || turn.TurnSeq != 8 {
		t.Fatalf("migrated sequence: %#v %v", turn, err)
	}
	messages, _, err := reopened.Messages(ctx, owner, c.ID, 1, 10, "")
	if err != nil || len(messages) != 2 {
		t.Fatal("migration lost transcript", err)
	}
	if _, err = reopened.db.Exec(`DELETE FROM users WHERE id=?`, owner); err != nil {
		t.Fatal("owned cascade blocked account deletion", err)
	}
	var n int
	if err = reopened.db.QueryRow(`SELECT COUNT(*) FROM mobile_turns`).Scan(&n); err != nil || n != 0 {
		t.Fatal("orphan turn")
	}
}
func TestAcceptIsAtomicAndIdempotent(t *testing.T) {
	r, path, owner, other, key := fixture(t)
	c := create(t, r, owner)
	in := input("private canary")
	first, replayed, err := r.Accept(ctx, owner, c.ID, in)
	if err != nil || replayed {
		t.Fatalf("first: %#v %v", first, err)
	}
	title := "renamed"
	settings := Settings{ProfileID: "different", MemoryMode: "account"}
	if _, err = r.Update(ctx, owner, c.ID, 1, &title, &settings); err != nil {
		t.Fatal(err)
	}
	r.Close()
	r, err = OpenExisting(ctx, path, key)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	again, replayed, err := r.Accept(ctx, owner, c.ID, in)
	if err != nil || !replayed || again.ID != first.ID || again.AcceptedSettings != first.AcceptedSettings {
		t.Fatalf("restart retry: %#v %v", again, err)
	}
	changed := in
	changed.Text = "different"
	if _, _, err = r.Accept(ctx, owner, c.ID, changed); !errors.Is(err, ErrIdempotency) {
		t.Fatal(err)
	}
	if _, _, err = r.Accept(ctx, other, c.ID, in); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err = r.GetTurnByClientID(ctx, other, c.ID, in.ClientMessageID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	got, err := r.GetTurnByClientID(ctx, owner, c.ID, in.ClientMessageID)
	if err != nil || got.ID != first.ID {
		t.Fatal(err)
	}
	for _, table := range []string{"mobile_turns", "mobile_messages"} {
		var n int
		if err = r.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s=%d %v", table, n, err)
		}
	}
	if _, err = r.Clear(ctx, owner, c.ID, 1); err != nil {
		t.Fatal(err)
	}
	again, replayed, err = r.Accept(ctx, owner, c.ID, in)
	if err != nil || !replayed || again.ID != first.ID || again.State != "cancelled" {
		t.Fatalf("clear retry: %#v %v", again, err)
	}
	var n int
	if err = r.db.QueryRow(`SELECT COUNT(*) FROM mobile_messages`).Scan(&n); err != nil || n != 0 {
		t.Fatal("retry resurrected cleared text")
	}
}
func TestAcceptRollbackIncludesEventAndCounters(t *testing.T) {
	r, _, owner, _, _ := fixture(t)
	c := create(t, r, owner)
	in := input("secret")
	if _, err := r.db.Exec(`CREATE TRIGGER reject_event BEFORE INSERT ON mobile_events BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Accept(ctx, owner, c.ID, in); err == nil {
		t.Fatal("failure not injected")
	}
	for _, table := range []string{"mobile_turns", "mobile_messages", "mobile_transcript_turns", "mobile_events", "mobile_event_sequences"} {
		var n int
		want := 0
		if table == "mobile_events" || table == "mobile_event_sequences" {
			want = 1
		}
		if err := r.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil || n != want {
			t.Fatalf("partial commit %s=%d %v", table, n, err)
		}
	}
	if _, err := r.db.Exec(`DROP TRIGGER reject_event`); err != nil {
		t.Fatal(err)
	}
	turn, _, err := r.Accept(ctx, owner, c.ID, in)
	if err != nil || turn.TurnSeq != 1 || turn.AdmissionSeq != 2 {
		t.Fatalf("counter rollback %#v %v", turn, err)
	}
}
func TestConcurrentSameMessageAcrossConnections(t *testing.T) {
	r, path, owner, _, key := fixture(t)
	other, err := OpenExisting(ctx, path, key)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	c := create(t, r, owner)
	in := input("once")
	var wg sync.WaitGroup
	wg.Add(2)
	start := make(chan struct{})
	turns := make([]Turn, 2)
	errs := make([]error, 2)
	replays := make([]bool, 2)
	for i, repo := range []*Repository{r, other} {
		go func(i int, repo *Repository) {
			defer wg.Done()
			<-start
			turns[i], replays[i], errs[i] = repo.Accept(ctx, owner, c.ID, in)
		}(i, repo)
	}
	close(start)
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || turns[0].ID != turns[1].ID || replays[0] == replays[1] {
		t.Fatalf("duplicate race %v %v %v", turns, errs, replays)
	}
}
func TestOwnerQueueQuotaDuplicateFirstAndActiveLimit(t *testing.T) {
	r, _, owner, other, _ := fixture(t)
	convs := []Conversation{create(t, r, owner), create(t, r, owner), create(t, r, owner)}
	first := input("first")
	if _, _, err := r.Accept(ctx, owner, convs[0].ID, first); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if _, _, err := r.Accept(ctx, owner, convs[(i+1)%3].ID, input("queued")); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := r.Accept(ctx, owner, convs[0].ID, input("overflow")); !errors.Is(err, ErrQueueFull) {
		t.Fatal(err)
	}
	if _, replay, err := r.Accept(ctx, owner, convs[0].ID, first); err != nil || !replay {
		t.Fatal("quota prevented duplicate lookup")
	}
	for i := 0; i < 2; i++ {
		turn, err := r.ClaimNext(ctx, owner)
		if err != nil || turn.State != "running" {
			t.Fatalf("claim %v %v", turn, err)
		}
	}
	if _, err := r.ClaimNext(ctx, owner); !errors.Is(err, ErrNoTurn) {
		t.Fatal("active limit not enforced")
	}
	otherConv := create(t, r, other)
	if _, _, err := r.Accept(ctx, other, otherConv.ID, input("other owner")); err != nil {
		t.Fatal("cross-owner quota")
	}
}

func TestConcurrentFreshAdmissionHonorsOwnerBound(t *testing.T) {
	r, path, owner, _, key := fixture(t)
	second, err := OpenExisting(ctx, path, key)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	c := create(t, r, owner)
	start := make(chan struct{})
	errs := make([]error, 8)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			repo := r
			if i%2 == 1 {
				repo = second
			}
			_, _, errs[i] = repo.Accept(ctx, owner, c.ID, input("different client ID"))
		}(i)
	}
	close(start)
	wg.Wait()
	accepted := 0
	for _, err := range errs {
		if err == nil {
			accepted++
		} else if !errors.Is(err, ErrQueueFull) {
			t.Fatal(err)
		}
	}
	if accepted != 5 {
		t.Fatalf("concurrent queue accepted %d", accepted)
	}
	if _, err = r.ClaimNext(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if _, err = second.ClaimNext(ctx, owner); !errors.Is(err, ErrNoTurn) {
		t.Fatal("same-conversation predecessor ordering violated", err)
	}
}

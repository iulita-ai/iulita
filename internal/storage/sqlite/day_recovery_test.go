package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/dayview"
)

func TestDaySchemaUpgradesLegacyOperationsAndRestarts(t *testing.T) {
	s, e := New(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	ctx := context.Background()
	if _, e = s.db.ExecContext(ctx, "CREATE TABLE operations(id TEXT PRIMARY KEY, card_id TEXT, owner TEXT, status TEXT, created_at TIMESTAMP)"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.db.ExecContext(ctx, "CREATE TABLE cards(id TEXT PRIMARY KEY, owner TEXT, instance TEXT, chat BIGINT, thread INTEGER, message INTEGER, revision BIGINT, items_json TEXT, expires TIMESTAMP)"); e != nil {
		t.Fatal(e)
	}
	if e = s.EnsureDaySchema(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.EnsureDaySchema(ctx); e != nil {
		t.Fatal(e)
	}
	op := &dayview.Operation{ID: "op", Owner: "alice", Conversation: "conv", Status: "unknown_outcome", Instance: "bot-a", ApprovedAt: time.Now(), CommandJSON: `{"action":"complete","item":{"id":"task"}}`, CreatedAt: time.Now()}
	if ok, checkErr := s.ClaimDayOperation(ctx, op); checkErr != nil || !ok {
		t.Fatal(checkErr)
	}
	if e = s.ApplyDayVerification(ctx, "bob", op.ID, op.Status, dayview.EffectObserved); e == nil {
		t.Fatal("foreign verification accepted")
	}
	if e = s.ApplyDayVerification(ctx, "alice", op.ID, op.Status, dayview.EffectObserved); e != nil {
		t.Fatal(e)
	}
	ops, e := s.ListDayOperations(ctx, "alice", "conv")
	if e != nil || len(ops) != 1 || ops[0].Status != "effect_observed" {
		t.Fatal(ops, e)
	}
	ops, e = s.ListDayOperations(ctx, "bob", "conv")
	if e != nil || len(ops) != 0 {
		t.Fatal("cross-owner operation leak")
	}
}
func TestRetentionDropsExpiredPayloadPreservesUnknownAuthority(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if e := s.EnsureDaySchema(ctx); e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	c := &dayview.Card{ID: "expired", Owner: "alice", ItemsJSON: "PRIVATE CARD", DesiredState: "PRIVATE VIEW", Expires: now.Add(-time.Hour)}
	if e := s.SaveDayCard(ctx, c); e != nil {
		t.Fatal(e)
	}
	cmd, _ := json.Marshal(dayview.Command{Item: dayview.Item{ID: "task", Title: "PRIVATE TITLE", Source: "todoist"}, Action: "complete"})
	op := &dayview.Operation{ID: "uncertain", Owner: "alice", Conversation: "conv", Status: "unknown_outcome", Instance: "bot-a", ApprovedAt: time.Now(), CommandJSON: string(cmd), CreatedAt: now.Add(-31 * 24 * time.Hour)}
	if _, e := s.ClaimDayOperation(ctx, op); e != nil {
		t.Fatal(e)
	}
	if e := s.CleanupDayState(ctx, now, 100); e != nil {
		t.Fatal(e)
	}
	c, e := s.GetDayCard(ctx, c.ID)
	if e != nil || c.ItemsJSON != "" || c.DesiredState != "" {
		t.Fatal("expired private projection retained")
	}
	ops, e := s.ListDayOperations(ctx, "alice", "conv")
	if e != nil || len(ops) != 1 || strings.Contains(ops[0].CommandJSON, "PRIVATE TITLE") || !strings.Contains(ops[0].CommandJSON, "task") {
		t.Fatal("retention lost recovery authority or retained private title")
	}
	if ok, e := s.ClaimDayOperation(ctx, op); e != nil || ok {
		t.Fatal("retention allowed replay")
	}
}

func TestUnresolvedResourceBlocksNewCardAndChangedDueUntilExplicitResolution(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if e := s.EnsureDaySchema(ctx); e != nil {
		t.Fatal(e)
	}
	cmd := dayview.Command{Item: dayview.Item{ID: "task", Source: "provider", Connection: "pinned", ListID: "list"}, Action: "reschedule", Due: time.Now().Add(time.Hour)}
	raw, _ := json.Marshal(cmd)
	op := &dayview.Operation{ID: "first", CardID: "card-a", Owner: "alice", Conversation: "conv", Instance: "bot", Status: "running", CommandJSON: string(raw), ApprovedAt: time.Now(), CreatedAt: time.Now()}
	if ok, e := s.ClaimDayOperation(ctx, op); e != nil || !ok {
		t.Fatal(e)
	}
	if e := s.FinishDayOperation(ctx, op.ID, "unknown_outcome"); e != nil {
		t.Fatal(e)
	}
	cmd.Due = cmd.Due.Add(time.Hour)
	raw, _ = json.Marshal(cmd)
	next := *op
	next.ID = "second"
	next.CardID = "new-card"
	next.CommandJSON = string(raw)
	if ok, e := s.ClaimDayOperation(ctx, &next); e == nil || ok {
		t.Fatal("new card or changed due bypassed unknown resource guard")
	}
	if e := s.ResolveDayOperation(ctx, "bob", "conv", op.ID, "unknown_outcome"); e == nil {
		t.Fatal("foreign resolution accepted")
	}
	if e := s.ResolveDayOperation(ctx, "alice", "conv", op.ID, "unknown_outcome"); e != nil {
		t.Fatal(e)
	}
	if ok, e := s.ClaimDayOperation(ctx, &next); e != nil || !ok {
		t.Fatal("explicitly resolved resource remained blocked", e)
	}
	if ok, e := s.ClaimDayOperation(ctx, op); e != nil || ok {
		t.Fatal("old operation replayed after resolution")
	}
}

func TestResourceClaimConcurrentOnlyOneAccepted(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if e := s.EnsureDaySchema(ctx); e != nil {
		t.Fatal(e)
	}
	cmd, _ := json.Marshal(dayview.Command{Item: dayview.Item{ID: "shared-resource", Source: "provider"}, Action: "complete"})
	accepted := make(chan bool, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			op := &dayview.Operation{ID: fmt.Sprintf("claim-%d", i), Owner: "alice", Conversation: "conv", Instance: "bot", Status: "running", CommandJSON: string(cmd), ApprovedAt: time.Now(), CreatedAt: time.Now()}
			ok, _ := s.ClaimDayOperation(ctx, op)
			accepted <- ok
		}(i)
	}
	wg.Wait()
	close(accepted)
	count := 0
	for ok := range accepted {
		if ok {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("accepted %d writes to one unresolved resource", count)
	}
}

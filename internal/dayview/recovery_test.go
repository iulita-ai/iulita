package dayview

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

type recoveryMemory struct{ operations []Operation }

func (s *recoveryMemory) ListDayOperations(context.Context, string, string) ([]Operation, error) {
	return s.operations, nil
}
func (s *recoveryMemory) ApplyDayVerification(_ context.Context, _, id, expected string, v Verification) error {
	for i := range s.operations {
		if s.operations[i].ID == id && s.operations[i].Status == expected {
			s.operations[i].Status = string(v)
			s.operations[i].LastCheckedAt = time.Now()
		}
	}
	return nil
}

type verifierFixture struct {
	reads, writes int
	outcome       Verification
}

func (*verifierFixture) Name() string { return "fixture" }
func (*verifierFixture) List(context.Context, string, time.Time, *time.Location) ([]Item, error) {
	return nil, nil
}
func (s *verifierFixture) Change(context.Context, string, Item, string, time.Time) error {
	s.writes++
	return nil
}
func (s *verifierFixture) Verify(context.Context, string, Item, string, time.Time) (Verification, error) {
	s.reads++
	return s.outcome, nil
}
func TestRecoveryObservesEffectNeverRetriesMutation(t *testing.T) {
	raw, _ := json.Marshal(Command{Item: Item{ID: "task", Source: "fixture", Connection: "pinned"}, Action: "complete"})
	store := &recoveryMemory{operations: []Operation{{ID: "unknown", Owner: "alice", Conversation: "conv", Status: "unknown_outcome", CommandJSON: string(raw), CreatedAt: time.Now().Add(-time.Hour)}, {ID: "active", Owner: "alice", Conversation: "conv", Status: "running", CommandJSON: string(raw), CreatedAt: time.Now()}}}
	source := &verifierFixture{outcome: EffectObserved}
	got, e := RecoverOperations(context.Background(), store, &Service{Sources: []Source{source}}, "alice", "conv")
	if e != nil || source.writes != 0 || source.reads != 1 || got[0].Status != "effect_observed" || got[1].Status != "running" {
		t.Fatalf("unsafe recovery %+v %v", got, e)
	}
	_, e = RecoverOperations(context.Background(), store, &Service{Sources: []Source{source}}, "alice", "conv")
	if e != nil || source.reads != 1 {
		t.Fatal("terminal observed effect was retried")
	}
}

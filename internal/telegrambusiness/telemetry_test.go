package telegrambusiness

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type observationRecorder struct{ events []Observation }

func (r *observationRecorder) ObserveBusiness(o Observation) { r.events = append(r.events, o) }

func TestProcessingFailureExposesSafeOwnerStatusAndAggregateOutcome(t *testing.T) {
	f := setup(t, false)
	observer := &observationRecorder{}
	f.service.options.Observer = observer
	f.service.options.Budget = &testBudget{}
	f.service.generator = generatorFunc(func(context.Context, Scope, []Message) (string, error) {
		return "", fmt.Errorf("PRIVATE_PROVIDER_BODY: %w", ErrQuota)
	})
	if err := f.service.Process(context.Background(), f.scope); err == nil {
		t.Fatal("provider failure lost")
	}
	state, err := f.repo.Get(context.Background(), f.scope.Key)
	if err != nil {
		t.Fatal(err)
	}
	peer := state.Peers[f.scope.PeerID]
	if peer.LastProcessingState != "failed" || peer.LastProcessingReason != "budget_exhausted" || peer.LastProcessedAt.IsZero() {
		t.Fatalf("missing safe processing result: %s %s", peer.LastProcessingState, peer.LastProcessingReason)
	}
	foundFailure, foundReservation := false, false
	for _, event := range observer.events {
		if strings.Contains(event.Outcome, "PRIVATE") || strings.Contains(event.Kind, "PRIVATE") {
			t.Fatal("provider content escaped diagnostics")
		}
		foundFailure = foundFailure || event.Operation == "process" && event.Outcome == "quota"
		foundReservation = foundReservation || event.Operation == "generation" && event.Outcome == "reserved" && event.InputTokens == 16000 && event.OutputTokens == 1000
	}
	if !foundFailure || !foundReservation {
		t.Fatal("real processing path did not emit failure and reservation diagnostics")
	}
}

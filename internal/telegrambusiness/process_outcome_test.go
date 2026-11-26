package telegrambusiness

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestProcessOutcomeIsScopedSafeAndDoesNotRetryFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "ready", true: "failed"}[fail], func(t *testing.T) {
			f := setup(t, false)
			budget := &testBudget{}
			f.service.options.Budget = budget
			f.service.generator = generatorFunc(func(context.Context, Scope, []Message) (string, error) {
				if fail {
					return "", errors.New("private provider payload must never be persisted")
				}
				return "Owner draft", nil
			})
			resultErr := f.service.Process(context.Background(), f.scope)
			if (resultErr != nil) != fail {
				t.Fatal(resultErr)
			}
			st, _ := f.repo.Get(context.Background(), f.scope.Key)
			p := st.Peers[99]
			state, reason := "ready", "draft_ready"
			if fail {
				state, reason = "failed", "processing_failed"
			}
			if p.LastProcessingState != state || p.LastProcessingReason != reason || !p.LastProcessedAt.Equal(f.now) {
				t.Fatal("unsafe or missing processing outcome", p.LastProcessingState, p.LastProcessingReason, p.LastProcessedAt)
			}
			if e := f.service.Process(context.Background(), f.scope); e != nil {
				t.Fatal(e)
			}
			if budget.calls.Load() != 1 {
				t.Fatal("failed automatic revision retried")
			}
		})
	}
}
func TestStaleProcessCompletionCannotOverwriteNewRevisionOutcome(t *testing.T) {
	f := setup(t, false)
	f.service.options.Budget = &testBudget{}
	started, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	f.service.generator = generatorFunc(func(context.Context, Scope, []Message) (string, error) {
		close(started)
		<-release
		return "old draft", nil
	})
	done := make(chan error, 1)
	go func() { done <- f.service.Process(context.Background(), f.scope) }()
	<-started
	if e := f.service.Handle(context.Background(), Event{Key: f.scope.Key, OwnerID: "alice", PeerID: 99, Generation: "generation", UpdateID: 3, Kind: "message", DeferProcessing: true, Message: &Message{ID: 11, SenderID: 99, Date: f.now, Revision: 3, Text: "Newer source"}}); e != nil {
		t.Fatal(e)
	}
	newer := f.now.Add(time.Second)
	if e := f.repo.Update(context.Background(), f.scope.Key, func(st *State) error {
		p := st.Peers[99]
		p.LastProcessingState = "ready"
		p.LastProcessingReason = "sent"
		p.LastProcessedAt = newer
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	close(release)
	if e := <-done; e == nil {
		t.Fatal("stale generation succeeded")
	}
	st, _ := f.repo.Get(context.Background(), f.scope.Key)
	p := st.Peers[99]
	if p.LastProcessingState != "ready" || p.LastProcessingReason != "sent" || !p.LastProcessedAt.Equal(newer) {
		t.Fatal("old claim overwrote later outcome")
	}
}

func TestObservedMessageAgeEmitsOnlyForActualProcessAdmission(t *testing.T) {
	f := setup(t, false)
	observer := &observationRecorder{}
	f.service.options.Observer = observer
	f.service.options.Budget = &testBudget{}
	f.service.options.Now = func() time.Time { return f.now.Add(90 * time.Second) }
	f.service.generator = generatorFunc(func(context.Context, Scope, []Message) (string, error) { return "Owner draft", nil })
	for i := 0; i < 2; i++ {
		if e := f.service.Process(context.Background(), f.scope); e != nil {
			t.Fatal(e)
		}
	}
	count := 0
	for _, event := range observer.events {
		if event.Operation == "queue" && event.Kind == "candidate" {
			count++
			if event.Age != 90*time.Second || event.Outcome != "ok" {
				t.Fatal("incorrect observed source age", event)
			}
		}
	}
	if count != 1 {
		t.Fatal("deduplicated process emitted fake admission age", count)
	}
}

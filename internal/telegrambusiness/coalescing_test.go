package telegrambusiness

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestPeerPolicyPauseAndPurgePreserveConnectionAndNeighborRevisions(t *testing.T) {
	for _, operation := range []string{"configure", "pause", "purge"} {
		t.Run(operation, func(t *testing.T) {
			f := setup(t, false)
			other := Scope{Key: f.scope.Key, OwnerID: "alice", PeerID: 100}
			if checkErr := f.service.Configure(context.Background(), ConfigureInput{Scope: other, Policy: PeerPolicy{Enabled: true, Consent: true, Mode: ModeConfirm, AllowedMethods: []Method{SendText}}}); checkErr != nil {
				t.Fatal(checkErr)
			}
			before, _ := f.repo.Get(context.Background(), f.scope.Key)
			revision := before.Peers[99].Revision
			var e error
			switch operation {
			case "configure":
				policy := before.Peers[99].Policy
				policy.AutoReply = true
				e = f.service.Configure(context.Background(), ConfigureInput{Scope: f.scope, ExpectedRevision: revision, Policy: policy})
			case "pause":
				e = f.service.Pause(context.Background(), f.scope, revision, true)
			case "purge":
				e = f.service.PurgeRevision(context.Background(), f.scope, revision)
			}
			if e != nil {
				t.Fatal(e)
			}
			after, _ := f.repo.Get(context.Background(), f.scope.Key)
			if after.Connection.Epoch != before.Connection.Epoch || after.Peers[100].Revision != before.Peers[100].Revision {
				t.Fatal("peer operation invalidated account/neighbor scope")
			}
			if after.Peers[99].Revision != revision+1 {
				t.Fatal("peer operation did not increment exactly once")
			}
		})
	}
}

func TestNewIncomingDuringGenerationCoalescesLatestWithoutThirdMessage(t *testing.T) {
	f := setup(t, false)
	f.service.options.Budget = &testBudget{}
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	f.service.generator = generatorFunc(func(_ context.Context, _ Scope, messages []Message) (string, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			return "old result", nil
		}
		if messages[len(messages)-1].Text != "newest accepted request" {
			t.Error("coalescer lost latest accepted content")
		}
		return "latest result", nil
	})
	done := make(chan error, 1)
	go func() { done <- f.service.Process(context.Background(), f.scope) }()
	<-started
	if checkErr := f.service.Handle(context.Background(), Event{Key: f.scope.Key, OwnerID: "alice", PeerID: 99, Generation: "generation", UpdateID: 3, Kind: "message", Message: &Message{ID: 11, SenderID: 99, Date: f.now, Revision: 3, Text: "newest accepted request"}}); checkErr != nil {
		t.Fatal(checkErr)
	}
	if checkErr := f.service.Process(context.Background(), f.scope); checkErr != nil {
		t.Fatal(checkErr)
	}
	close(release)
	if checkErr := <-done; checkErr != nil {
		t.Fatal(checkErr)
	}
	st, _ := f.repo.Get(context.Background(), f.scope.Key)
	if calls.Load() != 2 || st.Peers[99].AutoRevision != st.Peers[99].Revision {
		t.Fatal("latest revision starved")
	}
	if len(st.Peers[99].Drafts) != 1 {
		t.Fatal("coalesced old result persisted")
	}
	for _, d := range st.Peers[99].Drafts {
		if d.Text != "latest result" {
			t.Fatal(d.Text)
		}
	}
}

func TestFutureIncomingDoesNotExtendReplyWindow(t *testing.T) {
	f := setup(t, false)
	if checkErr := f.repo.Update(context.Background(), f.scope.Key, func(st *State) error { st.Peers[99].LastIncomingAt = f.now.Add(-25 * time.Hour); return nil }); checkErr != nil {
		t.Fatal(checkErr)
	}
	if e := f.service.Handle(context.Background(), Event{Key: f.scope.Key, OwnerID: "alice", PeerID: 99, Generation: "generation", UpdateID: 3, Kind: "message", Message: &Message{ID: 11, SenderID: 99, Date: f.now.Add(time.Second), Revision: 3, Text: "future"}}); !errors.Is(e, ErrStale) {
		t.Fatal(e)
	}
	st, _ := f.repo.Get(context.Background(), f.scope.Key)
	if !st.Peers[99].LastIncomingAt.Equal(f.now.Add(-25 * time.Hour)) {
		t.Fatal("future source extended reply window")
	}
	for _, m := range st.Peers[99].Messages {
		if m.ID == 11 {
			t.Fatal("future source retained")
		}
	}
}

func TestPurgeDuringSendCannotRestorePrivateReceipt(t *testing.T) {
	f := setup(t, true)
	a := approve(t, f, prepare(t, f, "approved before purge"))
	started, release := make(chan struct{}), make(chan struct{})
	f.api.execute = func(context.Context, Request) (Receipt, error) {
		close(started)
		<-release
		return Receipt{MessageIDs: []int{101}, Result: json.RawMessage(`{"private":"must not return"}`)}, nil
	}
	done := make(chan error, 1)
	go func() { _, e := f.service.Execute(context.Background(), f.scope, a.ID); done <- e }()
	<-started
	if checkErr := f.service.Purge(context.Background(), f.scope); checkErr != nil {
		t.Fatal(checkErr)
	}
	close(release)
	<-done
	st, _ := f.repo.Get(context.Background(), f.scope.Key)
	result := st.Peers[99].Actions[a.ID]
	if result.Receipt != nil && len(result.Receipt.Result) > 0 {
		t.Fatal("late receipt resurrected private response")
	}
	if len(result.Params) > 0 {
		t.Fatal("purged intent restored")
	}
}

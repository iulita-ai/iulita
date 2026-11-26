package telegrambusiness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func TestCompactionChunksOversizedUTF8SourceAndInvalidatesOnEdit(t *testing.T) {
	f := setup(t, false)
	budget := &testBudget{}
	f.service.options.Budget = budget
	var compact, draft atomic.Int32
	f.service.generator = generatorFunc(func(ctx context.Context, scope Scope, messages []Message) (string, error) {
		settings, ok := GenerationSettingsFrom(ctx)
		if !ok || settings.Epoch == 0 || settings.ContextRevision == 0 || settings.RuntimeGeneration == "" || settings.Provider != "approved-provider" || contextBytes(messages) > 16000 {
			t.Error("missing immutable finite admission")
		}
		for mIndex := range messages {
			m := messages[mIndex]
			if !utf8.ValidString(m.Text) {
				t.Error("split UTF-8")
			}
		}
		if settings.Operation == "compact" {
			compact.Add(1)
			return "A short faithful peer summary", nil
		}
		draft.Add(1)
		if len(messages) < 2 || messages[0].Direction != "summary" {
			t.Error("draft omitted derived summary or raw remaining tail")
		}
		return "Draft from the bounded peer context", nil
	})
	if checkErr := f.service.Handle(context.Background(), Event{Key: f.scope.Key, OwnerID: "alice", PeerID: 99, Generation: "generation", UpdateID: 3, Kind: "message", Message: &Message{ID: 11, SenderID: 99, Date: f.now, Revision: 3, Text: strings.Repeat("л", 14000)}}); checkErr != nil {
		t.Fatal(checkErr)
	}
	d, e := f.service.Draft(context.Background(), f.scope)
	if e != nil || d == nil {
		t.Fatal(e)
	}
	if compact.Load() < 2 || draft.Load() != 1 || budget.calls.Load() != compact.Load()+draft.Load() {
		t.Fatal("compaction calls bypassed reservation", compact.Load(), draft.Load(), budget.calls.Load())
	}
	st, _ := f.repo.Get(context.Background(), f.scope.Key)
	if !validSummary(st.Peers[99]) {
		t.Fatal("summary not committed with source references")
	}
	if checkErr := f.service.Handle(context.Background(), Event{Key: f.scope.Key, OwnerID: "alice", PeerID: 99, Generation: "generation", UpdateID: 4, Kind: "edit", Message: &Message{ID: 11, SenderID: 99, Date: f.now, Revision: 4, Text: "Source corrected"}}); checkErr != nil {
		t.Fatal(checkErr)
	}
	st, _ = f.repo.Get(context.Background(), f.scope.Key)
	if st.Peers[99].Summary != nil {
		t.Fatal("source edit retained old summary")
	}
}

func TestFailedOrNonshrinkingCompactionPreservesRawContext(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "nonshrinking", true: "provider"}[fail], func(t *testing.T) {
			f := setup(t, false)
			if checkErr := f.repo.Update(context.Background(), f.scope.Key, func(st *State) error { st.Peers[99].Messages[0].Text = strings.Repeat("source fact ", 200); return nil }); checkErr != nil {
				t.Fatal(checkErr)
			}
			f.service.options.Budget = &testBudget{}
			f.service.generator = generatorFunc(func(context.Context, Scope, []Message) (string, error) {
				if fail {
					return "", errors.New("provider unavailable")
				}
				return strings.Repeat("x", 3000), nil
			})
			before, _ := f.repo.Get(context.Background(), f.scope.Key)
			if _, e := f.service.Compact(context.Background(), f.scope); e == nil {
				t.Fatal("invalid summary committed")
			}
			after, _ := f.repo.Get(context.Background(), f.scope.Key)
			if after.Peers[99].Summary != nil || after.Peers[99].Messages[0].Text != before.Peers[99].Messages[0].Text {
				t.Fatal("failed compaction damaged source")
			}
		})
	}
}

func TestAutomaticDraftDedupAndDefaultNoExternalWrites(t *testing.T) {
	f := setup(t, false)
	f.service.options.Budget = &testBudget{}
	var calls atomic.Int32
	f.service.generator = generatorFunc(func(context.Context, Scope, []Message) (string, error) { calls.Add(1); return "Review this draft", nil })
	for i := 0; i < 2; i++ {
		if checkErr := f.service.Process(context.Background(), f.scope); checkErr != nil {
			t.Fatal(checkErr)
		}
	}
	if calls.Load() != 1 || f.api.calls.Load() != 0 {
		t.Fatal("duplicate analysis or external write")
	}
	st, _ := f.repo.Get(context.Background(), f.scope.Key)
	if len(st.Peers[99].Drafts) != 1 || len(st.Peers[99].Actions) != 0 {
		t.Fatal("draft-only scheduled an account action")
	}
}

func TestAutoTextRequiresBothPoliciesAndIgnoresBotAndOwnerMessages(t *testing.T) {
	f := setup(t, true)
	f.service.options.Budget = &testBudget{}
	var generated atomic.Int32
	f.service.generator = generatorFunc(func(context.Context, Scope, []Message) (string, error) {
		generated.Add(1)
		return "Narrow automatic reply", nil
	})
	if e := f.repo.Update(context.Background(), f.scope.Key, func(st *State) error {
		st.Connection.Policy.Mode = ModeAuto
		st.Connection.Policy.AutoReply = true
		st.Connection.Policy.AutoRules = &AutoRules{Classes: []string{"short_question"}, Timezone: "UTC", StartHour: 9, EndHour: 18, MaxRepliesPerDay: 5}
		st.Peers[99].Policy.Mode = ModeAuto
		st.Peers[99].Policy.AutoReply = true
		st.Peers[99].Policy.AutoRules = &AutoRules{Classes: []string{"short_question"}, Timezone: "UTC", StartHour: 9, EndHour: 18, MaxRepliesPerDay: 5}
		st.Peers[99].Messages[0].Text = "Can you confirm?"
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if checkErr := f.service.Process(context.Background(), f.scope); checkErr != nil {
		t.Fatal(checkErr)
	}
	if f.api.calls.Load() != 1 {
		t.Fatal("explicit automatic policy did not send exactly once")
	}
	for i, kind := range []string{"bot", "owner"} {
		sender := int64(99)
		if kind == "owner" {
			sender = 42
		}
		if checkErr := f.service.Handle(context.Background(), Event{Key: f.scope.Key, OwnerID: "alice", PeerID: 99, Generation: "generation", UpdateID: int64(10 + i), Kind: "message", Message: &Message{ID: 20 + i, SenderID: sender, SenderIsBot: kind == "bot", Date: f.now, Revision: int64(10 + i), Text: "echo or manual takeover"}}); checkErr != nil {
			t.Fatal(checkErr)
		}
		if checkErr := f.service.Process(context.Background(), f.scope); checkErr != nil {
			t.Fatal(checkErr)
		}
	}
	if f.api.calls.Load() != 1 || generated.Load() != 1 {
		t.Fatal("bot or owner message generated a reply loop")
	}
	if checkErr := f.service.Handle(context.Background(), Event{Key: f.scope.Key, OwnerID: "alice", PeerID: 99, Generation: "generation", UpdateID: 20, Kind: "message", Message: &Message{ID: 30, SenderID: 99, Date: f.now, Revision: 20, Text: "Followup after owner takeover"}}); checkErr != nil {
		t.Fatal(checkErr)
	}
	if checkErr := f.service.Process(context.Background(), f.scope); checkErr != nil {
		t.Fatal(checkErr)
	}
	if f.api.calls.Load() != 1 || generated.Load() != 2 {
		t.Fatal("owner takeover did not persist draft-only policy")
	}
}

func TestRestartCancelsPreexistingApprovedIntent(t *testing.T) {
	f := setup(t, true)
	a := approve(t, f, prepare(t, f, "Do not replay on restart"))
	restarted := New(f.repo, f.api, nil, Options{LiveWrites: true, Now: func() time.Time { return f.now }})
	if _, e := restarted.Execute(context.Background(), f.scope, a.ID); e == nil {
		t.Fatal("old approval executed after runtime changed")
	}
	if f.api.calls.Load() != 0 {
		t.Fatal("restart replayed old external intent")
	}
	st, _ := f.repo.Get(context.Background(), f.scope.Key)
	if st.Peers[99].Actions[a.ID].State != "cancelled" {
		t.Fatal("restart failed to invalidate approval")
	}
}

func TestOwnerPreviewCASPreventsPurgeAndDraftOverwritingLaterContext(t *testing.T) {
	f := setup(t, false)
	st, _ := f.repo.Get(context.Background(), f.scope.Key)
	old := st.Peers[99].Revision
	if checkErr := f.service.Handle(context.Background(), Event{Key: f.scope.Key, OwnerID: "alice", PeerID: 99, Generation: "generation", UpdateID: 3, Kind: "message", Message: &Message{ID: 11, SenderID: 99, Date: f.now, Revision: 3, Text: "New accepted context"}}); checkErr != nil {
		t.Fatal(checkErr)
	}
	if e := f.service.PurgeRevision(context.Background(), f.scope, old); !errors.Is(e, ErrStale) {
		t.Fatal(e)
	}
	if _, e := f.service.CreateDraftRevision(context.Background(), f.scope, "stale editor", old); !errors.Is(e, ErrStale) {
		t.Fatal(e)
	}
	if _, e := f.service.Prepare(context.Background(), PrepareInput{Scope: f.scope, Method: SendText, Params: Params{"text": json.RawMessage(`"stale preview"`)}, ExpectedRevision: &old}); !errors.Is(e, ErrStale) {
		t.Fatal(e)
	}
	messages, e := f.service.Messages(context.Background(), f.scope)
	if e != nil || len(messages) != 2 {
		t.Fatal("stale destructive intent changed newer context")
	}
}

func TestDeleteStillErasesDerivedPayloadAfterLocalPause(t *testing.T) {
	f := setup(t, false)
	a := prepare(t, f, "derived private content")
	_ = a
	if checkErr := f.service.Pause(context.Background(), Scope{Key: f.scope.Key, OwnerID: "alice"}, currentEpoch(t, f), true); checkErr != nil {
		t.Fatal(checkErr)
	}
	if checkErr := f.service.Handle(context.Background(), Event{Key: f.scope.Key, OwnerID: "alice", PeerID: 99, Generation: "generation", UpdateID: 4, Kind: "delete", DeletedIDs: []int{10}}); checkErr != nil {
		t.Fatal(checkErr)
	}
	st, _ := f.repo.Get(context.Background(), f.scope.Key)
	if HasPrivateContent(st) {
		t.Fatal("paused source deletion retained derived private body")
	}
}

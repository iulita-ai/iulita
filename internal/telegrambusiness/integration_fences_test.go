package telegrambusiness

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestDeliveryFenceRemainsVisibleUntilFreshMetadata(t *testing.T) {
	f := setup(t, false)
	before := f.api.getCalls.Load()
	if checkErr := f.service.FenceDelivery(context.Background(), f.scope.Key, "generation"); checkErr != nil {
		t.Fatal(checkErr)
	}
	metadata, e := f.service.GetState(context.Background(), "alice", f.scope.Key)
	if e != nil || !metadata.Connection.NeedsRefresh {
		t.Fatal("metadata lost delivery refresh fence", e)
	}
	if _, e = f.service.Messages(context.Background(), f.scope); e == nil {
		t.Fatal("private retrieval crossed refresh fence")
	}
	if e = f.service.ReconcileGeneration(context.Background(), f.scope.Key, "generation"); e != nil {
		t.Fatal(e)
	}
	metadata, e = f.service.GetState(context.Background(), "alice", f.scope.Key)
	if e != nil || metadata.Connection.NeedsRefresh || f.api.getCalls.Load() <= before {
		t.Fatal("refresh did not require current upstream proof", e)
	}
}

func TestGenerationRequiresTokenOnlyAtAccountAndPeer(t *testing.T) {
	f := setup(t, false)
	budget := &testBudget{}
	f.service.options.Budget = budget
	f.service.generator = generatorFunc(func(context.Context, Scope, []Message) (string, error) {
		t.Fatal("provider called without account acknowledgement")
		return "", nil
	})
	if checkErr := f.repo.Update(context.Background(), f.scope.Key, func(st *State) error { st.Connection.Policy.TokenOnly = false; return nil }); checkErr != nil {
		t.Fatal(checkErr)
	}
	if _, e := f.service.Draft(context.Background(), f.scope); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
	if budget.calls.Load() != 0 {
		t.Fatal("budget reserved before both acknowledgements")
	}
}

func TestOutgoingTextAndEditsEnterLaterPromptWithoutTelegramEcho(t *testing.T) {
	f := setup(t, true)
	a := approve(t, f, prepare(t, f, "Sent answer visible to later context"))
	if _, e := f.service.Execute(context.Background(), f.scope, a.ID); e != nil {
		t.Fatal(e)
	}
	edit, e := f.service.Prepare(context.Background(), PrepareInput{Scope: f.scope, Method: EditText, Params: Params{"message_id": json.RawMessage(`100`), "text": json.RawMessage(`"Corrected sent answer"`)}})
	if e != nil {
		t.Fatal(e)
	}
	edit = approve(t, f, edit)
	if _, e = f.service.Execute(context.Background(), f.scope, edit.ID); e != nil {
		t.Fatal(e)
	}
	if e = f.service.Handle(context.Background(), Event{Key: f.scope.Key, OwnerID: "alice", PeerID: 99, Generation: "generation", UpdateID: 3, Kind: "message", Message: &Message{ID: 11, SenderID: 99, Date: f.now, Revision: 3, Text: "Continue from your answer"}}); e != nil {
		t.Fatal(e)
	}
	f.service.options.Budget = &testBudget{}
	f.service.generator = generatorFunc(func(_ context.Context, _ Scope, messages []Message) (string, error) {
		replies := 0
		for mIndex := range messages {
			m := messages[mIndex]
			if m.ID == 100 {
				replies++
				if m.Text != "Corrected sent answer" || m.Direction != "bot_outgoing" {
					t.Error("outgoing context lost/corrupted", m)
				}
			}
		}
		if replies != 1 {
			t.Error("outgoing edit duplicated or dropped", replies)
		}
		return "next draft", nil
	})
	if _, e = f.service.Draft(context.Background(), f.scope); e != nil {
		t.Fatal(e)
	}
}

func TestOutgoingMediaCaptionEntersLaterContext(t *testing.T) {
	f := setup(t, true)
	if e := f.repo.Update(context.Background(), f.scope.Key, func(st *State) error {
		p := st.Peers[99]
		p.Policy.AllowedMethods = append(p.Policy.AllowedMethods, SendPhoto)
		p.Messages[0].Media = []Media{{Kind: "photo", FileID: "same-peer-photo"}}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	a, e := f.service.Prepare(context.Background(), PrepareInput{Scope: f.scope, Method: SendPhoto, Params: Params{"photo": json.RawMessage(`"same-peer-photo"`), "caption": json.RawMessage(`"Approved media caption"`)}})
	if e != nil {
		t.Fatal(e)
	}
	a = approve(t, f, a)
	if _, e = f.service.Execute(context.Background(), f.scope, a.ID); e != nil {
		t.Fatal(e)
	}
	messages, e := f.service.Messages(context.Background(), f.scope)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for mIndex := range messages {
		m := messages[mIndex]
		if m.ID == 100 && m.Direction == "bot_outgoing" && m.Text == "Approved media caption" {
			found = true
		}
	}
	if !found {
		t.Fatal("approved outgoing caption absent without Telegram echo")
	}
}

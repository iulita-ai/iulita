package engine

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/mobile/conversations"
	"github.com/iulita-ai/iulita/internal/skill"
)

func TestLiveCatalogPinsAdmissionAndRechecksDispatch(t *testing.T) {
	var enabled atomic.Bool
	var writes atomic.Int32
	tool := Tool{Definition: llm.ToolDefinition{Name: "lookup"}, Revision: "binding-1", Effect: "read", Isolated: true, Execute: func(context.Context, conversations.Fence, json.RawMessage) (string, error) {
		writes.Add(1)
		return "ok", nil
	}}
	provider := providerFunc(func(_ context.Context, r llm.Request) (llm.Response, error) {
		if r.Message == "before enable" && len(r.Tools) != 0 {
			t.Error("new tool added to accepted turn")
		}
		if r.Message == "disable before dispatch" {
			enabled.Store(false)
			return llm.Response{ToolCalls: []llm.ToolCall{{ID: "x", Name: "lookup", Input: json.RawMessage(`{}`)}}}, nil
		}
		return llm.Response{Content: "done"}, nil
	})
	e, owner, c := engineFixture(t, provider, nil)
	e.Catalog = func(context.Context, string) ([]Tool, error) {
		if enabled.Load() {
			return []Tool{tool}, nil
		}
		return nil, nil
	}
	accepted(t, e, owner, c.ID, "before enable")
	enabled.Store(true)
	runOne(t, e, owner)
	turn := accepted(t, e, owner, c.ID, "disable before dispatch")
	runOne(t, e, owner)
	got, err := e.Repo.GetTurn(context.Background(), owner, c.ID, turn.ID)
	if err != nil || got.State != "failed" || writes.Load() != 0 {
		t.Fatal("disabled tool dispatched", got, err, writes.Load())
	}
}

func TestNativeActionScopeCannotBeExpandedByModel(t *testing.T) {
	var dispatched atomic.Int32
	tool := Tool{Definition: llm.ToolDefinition{Name: "tasks"}, Revision: "1", Effect: "unknown", Isolated: true, InteractiveOnly: true, RequiredScopes: []string{"chats:write"}, Actions: map[string]skill.ActionPolicy{"list": {Effect: "read", Scopes: []string{"assistant:services:read"}}, "delete": {Effect: "mutating", Confirmation: true, Scopes: []string{"assistant:services:write"}}}, Execute: func(context.Context, conversations.Fence, json.RawMessage) (string, error) {
		dispatched.Add(1)
		return "bad", nil
	}}
	provider := providerFunc(func(_ context.Context, r llm.Request) (llm.Response, error) {
		if len(r.ToolExchanges) > 0 {
			return llm.Response{Content: "denied"}, nil
		}
		return llm.Response{ToolCalls: []llm.ToolCall{{ID: "forged", Name: "tasks", Input: json.RawMessage(`{"action":"delete"}`)}}}, nil
	})
	e, owner, c := engineFixture(t, provider, []Tool{tool})
	a := Authority{OwnerID: owner, DeviceID: uuid.NewString(), FamilyID: uuid.NewString(), Scopes: []string{"chats:write", "assistant:services:read"}}
	e.VerifyAuthority = func(context.Context, Authority) error { return nil }
	turn, _, err := e.Accept(WithAuthority(context.Background(), a), owner, c.ID, conversations.Input{ClientMessageID: uuid.NewString(), Generation: 1, Text: "read only"})
	if err != nil {
		t.Fatal(err)
	}
	runOne(t, e, owner)
	got, err := e.Repo.GetTurn(context.Background(), owner, c.ID, turn.ID)
	if err != nil || got.State != "completed" || dispatched.Load() != 0 {
		t.Fatal("read token executed mutation", got, err)
	}
	a.PrincipalType = "service"
	if permits(a, tool) {
		t.Fatal("service inherited account integrations")
	}
}

func TestNativeMutationConfirmationBeforeDispatch(t *testing.T) {
	for _, test := range []string{"approve", "deny", "cancel", "revoke approver", "replay", "revoke in flight"} {
		t.Run(test, func(t *testing.T) {
			var dispatched atomic.Int32
			tool := Tool{Definition: llm.ToolDefinition{Name: "write"}, Revision: "1", Effect: "mutating", Isolated: true, InteractiveOnly: true, RequiredScopes: []string{"chats:write"}, Actions: map[string]skill.ActionPolicy{"": {Effect: "mutating", Confirmation: true}}, Execute: func(callCtx context.Context, _ conversations.Fence, _ json.RawMessage) (string, error) {
				dispatched.Add(1)
				if test == "revoke in flight" {
					<-callCtx.Done()
					return "", callCtx.Err()
				}
				return "done", nil
			}}
			provider := providerFunc(func(_ context.Context, r llm.Request) (llm.Response, error) {
				if len(r.ToolExchanges) > 0 {
					return llm.Response{Content: "finished"}, nil
				}
				return llm.Response{ToolCalls: []llm.ToolCall{{ID: "x", Name: "write", Input: json.RawMessage(`{"recipient":"exact","text":"exact body"}`)}}}, nil
			})
			e, owner, c := engineFixture(t, provider, []Tool{tool})
			a := Authority{OwnerID: owner, DeviceID: uuid.NewString(), FamilyID: uuid.NewString(), Scopes: []string{"chats:write"}}
			var revoked atomic.Bool
			approver := a
			approver.FamilyID = uuid.NewString()
			e.VerifyAuthority = func(_ context.Context, current Authority) error {
				if current.FamilyID == approver.FamilyID && revoked.Load() {
					return errors.New("revoked")
				}
				return nil
			}
			turn, _, err := e.Accept(WithAuthority(context.Background(), a), owner, c.ID, conversations.Input{ClientMessageID: uuid.NewString(), Generation: 1, Text: "write"})
			if err != nil {
				t.Fatal(err)
			}
			rt, err := e.Repo.AcquireRuntime(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer e.Repo.ReleaseRuntime(context.Background(), rt)
			claimed, f, err := e.Repo.Claim(context.Background(), rt, owner)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- e.process(context.Background(), claimed, f) }()
			var prompt conversations.Prompt
			for until := time.Now().Add(3 * time.Second); time.Now().Before(until); time.Sleep(10 * time.Millisecond) {
				snapshot, snapshotErr := e.Repo.Snapshot(context.Background(), owner, []string{c.ID}, 10, "")
				if snapshotErr != nil {
					t.Fatal(snapshotErr)
				}
				if len(snapshot.Items) > 0 && len(snapshot.Items[0].Prompts) > 0 {
					prompt = snapshot.Items[0].Prompts[0]
					break
				}
			}
			if prompt.ID == "" {
				t.Fatal("no durable confirmation")
			}
			if dispatched.Load() != 0 {
				t.Fatal("dispatched before approval")
			}
			if test == "cancel" {
				if _, err = e.Repo.Cancel(context.Background(), owner, c.ID, turn.ID, 1); err != nil {
					t.Fatal(err)
				}
			} else {
				decision := "approve"
				if test == "deny" {
					decision = "deny"
				}
				if test == "revoke approver" {
					revoked.Store(true)
				}
				p, decideErr := e.Repo.Decide(context.Background(), owner, a.DeviceID, c.ID, turn.ID, prompt.ID, 1, decision, prompt.OperationID, prompt.Nonce, prompt.Fingerprint, conversations.ApprovalAuthority(approver))
				if decideErr != nil {
					t.Fatal(decideErr)
				}
				if test == "replay" {
					if _, err = e.Repo.Decide(context.Background(), owner, a.DeviceID, c.ID, turn.ID, p.ID, 1, decision, prompt.OperationID, prompt.Nonce, prompt.Fingerprint, conversations.ApprovalAuthority(approver)); err == nil {
						t.Fatal("approval replay accepted")
					}
				}
			}
			if test == "revoke in flight" {
				for until := time.Now().Add(2 * time.Second); dispatched.Load() == 0 && time.Now().Before(until); time.Sleep(10 * time.Millisecond) {
				}
				if dispatched.Load() != 1 {
					t.Fatal("action not dispatched")
				}
				revoked.Store(true)
			}
			select {
			case processErr := <-done:
				if processErr != nil {
					t.Fatal(processErr)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("turn did not finish")
			}
			got, err := e.Repo.GetTurn(context.Background(), owner, c.ID, turn.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := int32(0)
			if test == "approve" || test == "replay" || test == "revoke in flight" {
				want = 1
			}
			if dispatched.Load() != want {
				t.Fatal("wrong dispatch count", dispatched.Load(), want)
			}
			if test == "revoke in flight" && got.State != "unknown_outcome" {
				t.Fatal("revoked dispatched action became replayable", got.State)
			}
			if test == "cancel" && got.State != "cancelled" {
				t.Fatal("undispatched mutation became ambiguous", got.State)
			}
		})
	}
}

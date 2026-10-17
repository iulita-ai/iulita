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
	"github.com/iulita-ai/iulita/internal/skill/interact"
)

func TestTypedInteractionApprovalBoundsActorCancelAndRevocation(t *testing.T) {
	for _, scenario := range []string{"approve", "deny", "cancel", "revoke", "timeout"} {
		t.Run(scenario, func(t *testing.T) {
			var effects atomic.Int32
			var revoked atomic.Bool
			tool := Tool{Definition: llm.ToolDefinition{Name: "strict", InputSchema: json.RawMessage(`{"type":"object"}`)}, Revision: "1", Effect: "unknown", Isolated: true, InteractiveOnly: true, RequiredScopes: []string{"chats:write"}, Execute: func(ctx context.Context, f conversations.Fence, _ json.RawMessage) (string, error) {
				if scenario == "timeout" {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, 150*time.Millisecond)
					defer cancel()
				}
				decision, err := interact.ConfirmerFrom(ctx).Confirm(ctx, interact.Confirmation{Nonce: "private-engine-nonce", OperationID: "private-engine-operation", Fingerprint: "private-engine-fingerprint", Preview: "safe exact operation", MaxBytes: 100, ExpiresAt: time.Now().Add(time.Minute)}, interact.Actor{UserID: f.OwnerID})
				if err != nil {
					return "", err
				}
				if decision.Approved {
					if decision.MaxBytes != 40 || decision.Actor.UserID != f.OwnerID {
						t.Error("wrong approved grant")
					}
					effects.Add(1)
				}
				return "done", nil
			}}
			provider := providerFunc(func(_ context.Context, r llm.Request) (llm.Response, error) {
				if len(r.ToolExchanges) > 0 {
					return llm.Response{Content: "done"}, nil
				}
				return llm.Response{ToolCalls: []llm.ToolCall{{ID: "x", Name: "strict", Input: json.RawMessage(`{}`)}}}, nil
			})
			e, owner, c := engineFixture(t, provider, []Tool{tool})
			a := Authority{OwnerID: owner, DeviceID: uuid.NewString(), FamilyID: uuid.NewString(), Scopes: []string{"chats:write"}}
			e.VerifyAuthority = func(context.Context, Authority) error {
				if revoked.Load() {
					return errors.New("revoked")
				}
				return nil
			}
			turn, _, err := e.Accept(WithAuthority(context.Background(), a), owner, c.ID, conversations.Input{ClientMessageID: uuid.NewString(), Generation: 1, Text: "strict"})
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
			var p conversations.Prompt
			for until := time.Now().Add(2 * time.Second); time.Now().Before(until); time.Sleep(5 * time.Millisecond) {
				snap, snapshotErr := e.Repo.Snapshot(context.Background(), owner, []string{c.ID}, 10, "")
				if snapshotErr != nil {
					t.Fatal(snapshotErr)
				}
				if len(snap.Items) > 0 && len(snap.Items[0].Prompts) > 0 {
					p = snap.Items[0].Prompts[0]
					break
				}
			}
			if p.ID == "" {
				t.Fatal("typed prompt not persisted")
			}
			if effects.Load() != 0 {
				t.Fatal("dispatch before approval")
			}
			decide := func(device, decision, nonce string, max int64) error {
				authority := a
				authority.DeviceID = device
				_, decideErr := e.Repo.DecideGranted(context.Background(), owner, device, c.ID, turn.ID, p.ID, 1, decision, p.OperationID, nonce, p.Fingerprint, max, conversations.ApprovalAuthority(authority))
				return decideErr
			}
			wrongFamily := a
			wrongFamily.FamilyID = uuid.NewString()
			if _, decideErr := e.Repo.DecideGranted(context.Background(), owner, a.DeviceID, c.ID, turn.ID, p.ID, 1, "approve", p.OperationID, p.Nonce, p.Fingerprint, 40, conversations.ApprovalAuthority(wrongFamily)); decideErr == nil {
				t.Fatal("different OAuth family reused approval")
			}
			if err = decide(uuid.NewString(), "approve", p.Nonce, 40); err == nil {
				t.Fatal("different device approved")
			}
			if err = decide(a.DeviceID, "approve", "wrong", 40); err == nil {
				t.Fatal("wrong nonce approved")
			}
			if err = decide(a.DeviceID, "approve", p.Nonce, 101); err == nil {
				t.Fatal("oversized byte grant")
			}
			if err = decide(a.DeviceID, "yes", p.Nonce, 0); err == nil {
				t.Fatal("chat text approved")
			}
			switch scenario {
			case "approve":
				if err = decide(a.DeviceID, "approve", p.Nonce, 40); err != nil {
					t.Fatal(err)
				}
				if err = decide(a.DeviceID, "approve", p.Nonce, 40); err == nil {
					t.Fatal("approval replay")
				}
			case "deny":
				if err = decide(a.DeviceID, "deny", p.Nonce, 0); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				if _, err = e.Repo.Cancel(context.Background(), owner, c.ID, turn.ID, 1); err != nil {
					t.Fatal(err)
				}
			case "revoke":
				revoked.Store(true)
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("wait survived cancellation/expiry")
			}
			expected := int32(0)
			if scenario == "approve" {
				expected = 1
			}
			if effects.Load() != expected {
				t.Fatal("wrong external effect", effects.Load(), expected)
			}
			if scenario == "cancel" || scenario == "revoke" {
				if err = decide(a.DeviceID, "approve", p.Nonce, 40); err == nil {
					t.Fatal("late approval accepted")
				}
			}
		})
	}
}

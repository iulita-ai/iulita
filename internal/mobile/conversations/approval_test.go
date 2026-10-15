package conversations

import (
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestConfirmationRequiresScopesFreshnessAndLiveApprover(t *testing.T) {
	r, _, owner, _, _ := fixture(t)
	c := create(t, r, owner)
	if _, _, err := r.Accept(ctx, owner, c.ID, input("sensitive")); err != nil {
		t.Fatal(err)
	}
	rt, err := r.AcquireRuntime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer r.ReleaseRuntime(ctx, rt)
	turn, f, err := r.Claim(ctx, rt, owner)
	if err != nil {
		t.Fatal(err)
	}
	fp := sha256.Sum256([]byte("exact action"))
	policy := Prompt{Kind: "confirmation", OperationID: uuid.NewString(), Fingerprint: fp[:], RequiredScopes: []string{"devices:manage"}, FreshAuthentication: true, ExpiresAt: time.Now().Add(time.Minute)}
	prompt, err := r.OpenPrompt(ctx, f, policy)
	if err != nil {
		t.Fatal(err)
	}
	actor := ApprovalAuthority{OwnerID: owner, DeviceID: uuid.NewString(), FamilyID: uuid.NewString(), Scopes: []string{"chats:write"}, AuthTime: time.Now().Unix()}
	decide := func(a ApprovalAuthority, decision string) (Prompt, error) {
		return r.Decide(ctx, owner, a.DeviceID, c.ID, turn.ID, prompt.ID, 1, decision, prompt.OperationID, prompt.Nonce, fp[:], a)
	}
	if _, err = decide(actor, "approve"); !errors.Is(err, ErrApproval) {
		t.Fatal("chat-only token confirmed system action", err)
	}
	actor.Scopes = []string{"devices:manage"}
	actor.AuthTime = time.Now().Add(-6 * time.Minute).Unix()
	if _, err = decide(actor, "approve"); !errors.Is(err, ErrApproval) {
		t.Fatal("stale credentials confirmed system action", err)
	}
	actor.AuthTime = time.Now().Unix()
	approved, err := decide(actor, "approve")
	if err != nil || approved.Approver == nil || approved.Approver.FamilyID != actor.FamilyID {
		t.Fatal("approver binding not persisted", approved, err)
	}
	called := 0
	revoked := false
	verifier := func(a ApprovalAuthority) error {
		called++
		if a.FamilyID != actor.FamilyID {
			t.Fatal("approval authority substituted")
		}
		if revoked {
			return errors.New("family revoked")
		}
		return nil
	}
	if err = r.PhoneConsent(ctx, f, prompt.OperationID, fp[:], verifier); err != nil || called != 1 {
		t.Fatal("live approval refused", err)
	}
	revoked = true
	if err = r.PhoneConsent(ctx, f, prompt.OperationID, fp[:], verifier); !errors.Is(err, ErrFenced) {
		t.Fatal("revoked approver accepted", err)
	}
	policy.OperationID = uuid.NewString()
	prompt, err = r.OpenPrompt(ctx, f, policy)
	if err != nil {
		t.Fatal(err)
	}
	actor.Scopes = []string{"chats:write"}
	actor.AuthTime = 1
	denied, err := decide(actor, "deny")
	if err != nil || denied.State != "rejected" {
		t.Fatal("authority reduction requires elevated scopes", denied, err)
	}
}

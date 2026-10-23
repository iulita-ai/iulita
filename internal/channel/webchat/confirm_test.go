package webchat

import (
	"context"
	"testing"

	"github.com/iulita-ai/iulita/internal/skill/interact"
)

func TestSSHConfirmationOffersOnlyOneShotChoices(t *testing.T) {
	opts := webchatConfirmOptions(interact.ConfirmSSHCommand)
	if len(opts) != 2 || opts[0].ID != "confirm_approve" || opts[1].ID != "confirm_deny" {
		t.Fatalf("SSH choices: %+v", opts)
	}
	if got := len(webchatConfirmOptions(interact.ConfirmMutation)); got != 4 {
		t.Fatal("HTTP standing choices changed")
	}
}
func TestPrivateResultRequiresVerifiedConfirmationAndIsOneShot(t *testing.T) {
	c := &webchatConfirmer{}
	if err := c.DeliverPrivateResult(context.Background(), interact.Actor{UserID: "owner"}, "op", "secret"); err == nil {
		t.Fatal("delivery without confirmation")
	}
	calls := 0
	c.privateDeliveries = map[string]func(context.Context, interact.Actor, string, string) error{"op": func(_ context.Context, _ interact.Actor, _, _ string) error { calls++; return nil }}
	if err := c.DeliverPrivateResult(context.Background(), interact.Actor{UserID: "owner"}, "op", "secret"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeliverPrivateResult(context.Background(), interact.Actor{UserID: "owner"}, "op", "secret"); err == nil {
		t.Fatal("replayed delivery")
	}
	if calls != 1 {
		t.Fatal("unexpected delivery calls")
	}
}

package auth

import (
	"context"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
)

func TestRefreshCannotUpgradeRevokedOrLegacyEpoch(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	user := &domain.User{ID: "epoch-owner", Username: "epoch-owner", SecurityEpoch: 1}
	if err := store.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	current, err := store.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	token, err := svc.createToken(current, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if revokeErr := store.RevokeUserSessions(ctx, user.ID); revokeErr != nil {
		t.Fatal(revokeErr)
	}
	if _, refreshErr := svc.RefreshToken(ctx, token); refreshErr == nil {
		t.Fatal("revoked signed token upgraded to current epoch")
	}
	legacy := *current
	legacy.SecurityEpoch = 0
	legacyToken, err := svc.createToken(&legacy, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	refreshed, err := svc.RefreshToken(ctx, legacyToken)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := svc.ValidateToken(refreshed)
	if err != nil {
		t.Fatal(err)
	}
	if claims.SecurityEpoch != 0 {
		t.Fatal("legacy refresh gained account capability without login")
	}
}

package browseroauth

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
)

type revocationGuardAccounts struct {
	Accounts
	entered chan struct{}
}

func (a revocationGuardAccounts) Guard(ctx context.Context, owner string, fn func(Account) error) error {
	close(a.entered)
	return a.Accounts.Guard(ctx, owner, fn)
}

func serviceForRevocation(t *testing.T, f *fixture) (clientSecret, accessToken string) {
	t.Helper()
	secret, err := oauthstore.Secret()
	if err != nil {
		t.Fatal(err)
	}
	registration := oauthstore.ClientRegistration{ClientID: "revocation-job", Name: "Revocation job", Kind: "service", OwnerID: f.user.ID}
	if err = f.store.RegisterOAuthClient(testContext, registration, secret); err != nil {
		t.Fatal(err)
	}
	response := f.serviceRequest("/oauth2/token", url.Values{"grant_type": {"client_credentials"}}, registration.ClientID, secret)
	var reply oauthstore.TokenReply
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &reply) != nil || reply.AccessToken == "" {
		t.Fatalf("service issuance: %d %s", response.Code, response.Body.String())
	}
	return secret, reply.AccessToken
}

func TestServiceRevocationWaitsForAccountReservation(t *testing.T) {
	f := setup(t)
	secret, token := serviceForRevocation(t, f)
	reserved := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseReservation := func() { releaseOnce.Do(func() { close(release) }) }
	reservationDone := make(chan error, 1)
	go func() {
		reservationDone <- f.users.WithUserSecurity(testContext, f.user.ID, func(*domain.User) error {
			close(reserved)
			<-release
			return nil
		})
	}()
	defer func() {
		releaseReservation()
		if err := <-reservationDone; err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-reserved:
	case <-time.After(3 * time.Second):
		t.Fatal("account reservation unavailable")
	}
	entered := make(chan struct{})
	f.s.config.Accounts = revocationGuardAccounts{Accounts: f.s.config.Accounts, entered: entered}
	responses := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		responses <- f.serviceRequest("/oauth2/revoke", url.Values{"token": {token}}, "revocation-job", secret)
	}()
	select {
	case <-entered:
	case response := <-responses:
		t.Fatalf("revoke bypassed account reservation: %d", response.Code)
	case <-time.After(3 * time.Second):
		t.Fatal("revoke did not enter account guard")
	}
	select {
	case response := <-responses:
		t.Fatalf("revoke acknowledged before reserved transaction finished: %d", response.Code)
	default:
	}
	if _, err := f.store.ValidateAccess(testContext, token); err != nil {
		t.Fatal("revoke mutated grant while account reservation was held", err)
	}
	releaseReservation()
	select {
	case response := <-responses:
		if response.Code != 200 {
			t.Fatalf("revoke after reservation: %d %s", response.Code, response.Body.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("revoke remained blocked")
	}
	if _, err := f.store.ValidateAccess(testContext, token); err == nil {
		t.Fatal("revoked grant remains active")
	}
}

func TestDisabledAccountCanRevokeServiceGrant(t *testing.T) {
	f := setup(t)
	secret, token := serviceForRevocation(t, f)
	f.user.Disabled = true
	if err := f.users.UpdateUser(testContext, f.user); err != nil {
		t.Fatal(err)
	}
	response := f.serviceRequest("/oauth2/revoke", url.Values{"token": {token}}, "revocation-job", secret)
	if response.Code != 200 {
		t.Fatalf("disabled account could not revoke: %d %s", response.Code, response.Body.String())
	}
	if _, err := f.store.ValidateAccess(testContext, token); err == nil {
		t.Fatal("disabled owner's grant remains live after explicit revoke")
	}
}

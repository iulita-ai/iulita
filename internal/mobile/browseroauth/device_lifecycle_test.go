package browseroauth

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
)

func TestDeviceApprovalSurvivesRestartAndConsumesOnce(t *testing.T) {
	f := setup(t)
	_, jwk := installation(t)
	device := startCLI(t, f, jwk)
	approveCLI(t, f, device, "approve")
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := oauthstore.Open(testContext, f.path, f.realm)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	f.store = reopened
	f.s, err = New(Config{Store: reopened, Accounts: PasswordAccounts{f.users}, Scopes: []string{"chats:read", "chats:write"}})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan int, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response := f.request("/oauth2/token", devicePoll(device, jwk), nil)
			if response.Code == 200 {
				var token oauthstore.TokenReply
				if json.Unmarshal(response.Body.Bytes(), &token) != nil || token.AccessToken == "" {
					results <- 500
					return
				}
			}
			results <- response.Code
		}()
	}
	wg.Wait()
	close(results)
	issued := 0
	for code := range results {
		if code == 200 {
			issued++
		} else if code != 400 {
			t.Fatal("unexpected concurrent response", code)
		}
	}
	if issued != 1 {
		t.Fatal("device authorization consumed more than once", issued)
	}
}
func TestCLIRecoveryUsesOwnKeyAndFreshBrowserLogin(t *testing.T) {
	f := setup(t)
	key, jwk := installation(t)
	device := startCLI(t, f, jwk)
	approveCLI(t, f, device, "approve")
	pending := f.exchange(t, devicePoll(device, jwk))
	claims, err := f.store.ValidateAccess(testContext, pending.Access)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.FinishInstallation(testContext, claims, pending.Challenge.OperationID, pending.Challenge.Nonce, proof(t, key, pending.Challenge), &jwk, "Console"); err != nil {
		t.Fatal(err)
	}
	next, err := f.store.CreateDeviceAuthorization(testContext, "cli", "", jwk.Thumbprint())
	if err != nil {
		t.Fatal(err)
	}
	approveCLI(t, f, *next, "approve")
	provisional := f.exchange(t, devicePoll(*next, jwk))
	if provisional.Stage != oauthstore.Recovery || provisional.Scope != "" || provisional.Challenge == nil || provisional.Challenge.Purpose != "recover_device" {
		t.Fatal("CLI recovery authority", provisional)
	}
	recovered, err := f.store.ValidateAccess(testContext, provisional.Access)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.FinishInstallation(testContext, recovered, provisional.Challenge.OperationID, provisional.Challenge.Nonce, proof(t, key, provisional.Challenge), nil, ""); err != nil {
		t.Fatal("CLI recovery", err)
	}
}
func TestDeviceApprovalCredentialChangeInvalidatesGrant(t *testing.T) {
	f := setup(t)
	_, jwk := installation(t)
	device := startCLI(t, f, jwk)
	approveCLI(t, f, device, "approve")
	f.user.SecurityEpoch++
	if err := f.users.UpdateUser(testContext, f.user); err != nil {
		t.Fatal(err)
	}
	expectOAuthError(t, f.request("/oauth2/token", devicePoll(device, jwk), nil), "invalid_grant")
}

func TestDeviceVerificationReusesBrowserBindingAcrossFlows(t *testing.T) {
	f := setup(t)
	_, jwk := installation(t)
	first := startCLI(t, f, jwk)
	response := f.request("/oauth2/device?user_code="+first.UserCode, nil, nil)
	firstForm := fields(t, response)
	cookie := response.Result().Cookies()[0]
	second, err := f.store.CreateDeviceAuthorization(testContext, "cli", "", jwk.Thumbprint())
	if err != nil {
		t.Fatal(err)
	}
	response = f.request("/oauth2/device?user_code="+second.UserCode, nil, cookie)
	fields(t, response)
	if response.Result().Cookies()[0].Value != cookie.Value {
		t.Fatal("parallel device flow discarded browser binding")
	}
	if _, err = f.store.CheckBrowserFlow(testContext, cookie.Value, firstForm.Get("flow_id"), firstForm.Get("csrf"), "login"); err != nil {
		t.Fatal("first tab invalidated", err)
	}
}

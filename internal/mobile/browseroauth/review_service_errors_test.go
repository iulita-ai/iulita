package browseroauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
)

type unavailableGuardAccounts struct{ Accounts }

func (a unavailableGuardAccounts) Guard(context.Context, string, func(Account) error) error {
	return errors.New("injected account storage failure")
}

func checkServiceError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var result map[string]string
	if response.Code != status || json.Unmarshal(response.Body.Bytes(), &result) != nil || result["error"] != code {
		t.Fatalf("want %d %s; got %d %s", status, code, response.Code, response.Body.String())
	}
}
func TestServiceTokenErrorsDistinguishScopeAccountAndTemporaryFailure(t *testing.T) {
	for _, scenario := range []string{"scope", "disabled-account", "account-storage", "capacity"} {
		t.Run(scenario, func(t *testing.T) {
			f := setup(t)
			secret, _ := serviceForRevocation(t, f)
			form := url.Values{"grant_type": {"client_credentials"}}
			expectedStatus, expectedCode := 400, "invalid_scope"
			switch scenario {
			case "scope":
				form.Set("scope", "phone:ui")
			case "disabled-account":
				f.user.Disabled = true
				if err := f.users.UpdateUser(testContext, f.user); err != nil {
					t.Fatal(err)
				}
				expectedCode = "invalid_grant"
			case "account-storage":
				f.s.config.Accounts = unavailableGuardAccounts{Accounts: f.s.config.Accounts}
				expectedStatus, expectedCode = 503, "temporarily_unavailable"
			case "capacity":
				// Existing issuance from serviceForRevocation occupies the first live slot.
				if _, err := f.db.Exec(`INSERT INTO oauth_service_access(grant_id,client_id,principal_id,owner_id,client_epoch,owner_epoch,signing_epoch,scope,expires_at,revoked) SELECT 'retained-'||value,a.client_id,a.principal_id,a.owner_id,a.client_epoch,a.owner_epoch,a.signing_epoch,a.scope,a.expires_at,a.revoked FROM oauth_service_access a JOIN json_each(?) WHERE a.client_id='revocation-job'`, capacityValues()); err != nil {
					t.Fatal(err)
				}
				expectedStatus, expectedCode = 503, "temporarily_unavailable"
			}
			response := f.serviceRequest("/oauth2/token", form, "revocation-job", secret)
			checkServiceError(t, response, expectedStatus, expectedCode)
			if expectedStatus == 503 && response.Header().Get("Retry-After") == "" {
				t.Fatal("temporary failure lacks retry guidance")
			}
		})
	}
}
func capacityValues() string {
	values := make([]int, 127)
	for index := range values {
		values[index] = index
	}
	encoded, _ := json.Marshal(values)
	return string(encoded)
}

func TestServiceClientReauthenticationFailureIsTyped(t *testing.T) {
	f := setup(t)
	secret, _ := serviceForRevocation(t, f)
	replacement, _ := oauthstore.Secret()
	if err := f.store.ChangeOAuthClient(testContext, "revocation-job", replacement, false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.IssueServiceToken(testContext, "revocation-job", secret, ""); !errors.Is(err, oauthstore.ErrClientAuthentication) {
		t.Fatal("secret changed after owner lookup must be classified as authentication failure", err)
	}
}

func TestServiceRevocationStorageFailureIsRetryable(t *testing.T) {
	f := setup(t)
	secret, token := serviceForRevocation(t, f)
	f.s.config.Accounts = unavailableGuardAccounts{Accounts: f.s.config.Accounts}
	response := f.serviceRequest("/oauth2/revoke", url.Values{"token": {token}}, "revocation-job", secret)
	checkServiceError(t, response, 503, "temporarily_unavailable")
	if _, err := f.store.ValidateAccess(testContext, token); err != nil {
		t.Fatal("failed reservation partially revoked service grant", err)
	}
}
func TestServiceLookupStorageFailureIsNotBadCredential(t *testing.T) {
	f := setup(t)
	secret, _ := serviceForRevocation(t, f)
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ServiceClientOwner(testContext, "revocation-job", secret); err == nil || errors.Is(err, oauthstore.ErrClientAuthentication) {
		t.Fatal("storage failure masquerades as invalid client", err)
	}
}

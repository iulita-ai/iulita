package oauthstore

import (
	"errors"
	"testing"
)

func TestServiceCapacityCountsOnlyCurrentLiveAuthority(t *testing.T) {
	for _, change := range []string{"revoke", "rotate-secret", "owner-epoch", "signing-epoch"} {
		t.Run(change, func(t *testing.T) {
			f := fixture(t, Active)
			secret, err := Secret()
			if err != nil {
				t.Fatal(err)
			}
			if err = f.RegisterOAuthClient(ctx, ClientRegistration{ClientID: "capacity-service", Name: "Capacity service", Kind: "service", OwnerID: f.owner}, secret); err != nil {
				t.Fatal(err)
			}
			if err = f.SyncAccount(ctx, f.owner, "credentials-v1", true); err != nil {
				t.Fatal(err)
			}
			var first string
			for index := 0; index < 128; index++ {
				reply, issueErr := f.IssueServiceToken(ctx, "capacity-service", secret, "")
				if issueErr != nil {
					t.Fatalf("issue grant %d: %v", index, issueErr)
				}
				if index == 0 {
					first = reply.AccessToken
				}
			}
			if _, err = f.IssueServiceToken(ctx, "capacity-service", secret, ""); !errors.Is(err, ErrConflict) {
				t.Fatalf("live grant capacity was not enforced: %v", err)
			}
			switch change {
			case "revoke":
				err = f.RevokeServiceAccess(ctx, "capacity-service", secret, first)
			case "rotate-secret":
				secret, err = Secret()
				if err == nil {
					err = f.ChangeOAuthClient(ctx, "capacity-service", secret, false)
				}
			case "owner-epoch":
				err = f.SyncAccount(ctx, f.owner, "credentials-v2", true)
			case "signing-epoch":
				// Retained stale-epoch tombstones must not consume capacity. Real
				// offline rotation also revokes these rows; this isolates the fence.
				_, err = f.db.ExecContext(ctx, `UPDATE oauth_service_access SET signing_epoch=signing_epoch+1 WHERE client_id=?`, "capacity-service")
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.ValidateAccess(ctx, first); err == nil {
				t.Fatal("old authority remained valid")
			}
			fresh, issueErr := f.IssueServiceToken(ctx, "capacity-service", secret, "")
			if issueErr != nil {
				t.Fatalf("invalidated grants blocked replacement: %v", issueErr)
			}
			if _, err = f.ValidateAccess(ctx, fresh.AccessToken); err != nil {
				t.Fatalf("replacement authority invalid: %v", err)
			}
			if change == "revoke" {
				if _, err = f.IssueServiceToken(ctx, "capacity-service", secret, ""); !errors.Is(err, ErrConflict) {
					t.Fatalf("replacing one grant bypassed capacity: %v", err)
				}
			}
		})
	}
}

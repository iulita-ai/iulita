package oauthstore

import (
	"testing"
)

func TestBrowserMigrationPreservesExistingRealmAndEpochs(t *testing.T) {
	f := fixture(t, Pending)
	if err := f.BumpOwnerEpoch(ctx, f.owner, 1); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DROP INDEX oauth_family_device`, `DROP TABLE oauth_device_revocations`, `DROP TABLE oauth_completions`, `ALTER TABLE oauth_devices DROP COLUMN public_jwk`, `ALTER TABLE oauth_devices DROP COLUMN label`, `ALTER TABLE oauth_devices DROP COLUMN pending_expires_at`, `ALTER TABLE oauth_owners DROP COLUMN account_stamp`, `DROP TABLE oauth_browser_flows`, `DROP TABLE oauth_browser_limits`, `ALTER TABLE oauth_devices DROP COLUMN activated`, `DROP TABLE oauth_schema`, `CREATE TABLE oauth_schema(version INTEGER PRIMARY KEY CHECK(version=1))`, `INSERT INTO oauth_schema VALUES(1)`} {
		if _, err := f.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()
	reopened, err := Open(ctx, f.path, f.realm)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	family, err := reopened.Family(ctx, f.owner, f.family.ID)
	if err != nil || !family.Revoked {
		t.Fatal("migration reset revocation", err)
	}
	var epoch uint64
	if err = reopened.db.QueryRow(`SELECT epoch FROM oauth_owners WHERE id=?`, f.owner).Scan(&epoch); err != nil || epoch != 2 {
		t.Fatal("migration reset epoch", err)
	}
	var activated bool
	if err = reopened.db.QueryRow(`SELECT activated FROM oauth_devices WHERE id=?`, f.device).Scan(&activated); err != nil || activated {
		t.Fatal("migration invented device activation", err)
	}
	cookie, _ := Secret()
	challenge, _ := Secret()
	flow, csrf, err := reopened.CreateBrowserFlow(ctx, cookie, Authorization{ClientID: "mobile", RedirectURI: redirect, Scope: "chats:read", State: "long-random-state", Challenge: challenge, KeyThumbprint: challenge})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.CheckBrowserFlow(ctx, cookie, flow.ID, "", "login"); err == nil {
		t.Fatal("empty CSRF bypass")
	}
	if _, err = reopened.CheckBrowserFlow(ctx, cookie, flow.ID, csrf, "login"); err != nil {
		t.Fatal(err)
	}
}

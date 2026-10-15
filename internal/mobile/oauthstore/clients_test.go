package oauthstore

import "testing"

func TestClientPolicyV3MigrationPreservesAuthority(t *testing.T) {
	f := fixture(t, Pending)
	before, err := f.Family(ctx, f.owner, f.family.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`DROP TABLE oauth_service_access`, `DROP TABLE oauth_device_requests`, `DROP TABLE oauth_client_policy`, `DROP TABLE oauth_schema`, `CREATE TABLE oauth_schema(version INTEGER PRIMARY KEY CHECK(version=3))`, `INSERT INTO oauth_schema VALUES(3)`} {
		if _, err = f.db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()
	reopened, err := Open(ctx, f.path, f.realm)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	client, err := reopened.ClientPolicy(ctx, "mobile")
	if err != nil || client.Kind != "native" || client.Epoch != 1 || client.Disabled || client.OwnerID != "" || client.PrincipalID != "" {
		t.Fatal("native backfill", client, err)
	}
	after, err := reopened.Family(ctx, f.owner, f.family.ID)
	if err != nil || after.OwnerEpoch != before.OwnerEpoch || after.DeviceEpoch != before.DeviceEpoch || after.SigningEpoch != before.SigningEpoch || after.Revoked != before.Revoked || after.Stage != before.Stage {
		t.Fatal("migration altered existing family", err)
	}
	var version int
	if err = reopened.db.QueryRow(`SELECT version FROM oauth_schema`).Scan(&version); err != nil || version != 4 {
		t.Fatal("schema marker", version, err)
	}
	if err = reopened.RegisterOAuthClient(ctx, ClientRegistration{ClientID: "mobile", Name: "replacement", Kind: "cli"}, ""); err == nil {
		t.Fatal("registration replaced existing native client")
	}
}

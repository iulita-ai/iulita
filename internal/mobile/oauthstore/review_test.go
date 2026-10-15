package oauthstore

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ory/fosite"
)

func TestExpiredRetainedRefreshReplayRevokesFamily(t *testing.T) {
	f := fixture(t, Active)
	e := newEngine(f)
	first := issue(t, e)
	old := refresh(t, first)
	next, err := e.exchange(refreshForm(old))
	if err != nil {
		t.Fatal(err)
	}
	key := f.realm.digest("session.refresh", f.Strategy().RefreshTokenSignature(ctx, old))
	var data []byte
	if err = f.db.QueryRow(`SELECT data FROM oauth_sessions WHERE kind='refresh' AND key_hash=?`, key).Scan(&data); err != nil {
		t.Fatal(err)
	}
	var saved savedRequest
	if err = json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	expiry := time.Now().Add(-time.Second)
	saved.Session.SetExpiresAt(fosite.RefreshToken, expiry)
	data, _ = json.Marshal(saved)
	if _, err = f.db.Exec(`UPDATE oauth_sessions SET data=?,expires_at=? WHERE kind='refresh' AND key_hash=?`, data, expiry.Unix(), key); err != nil {
		t.Fatal(err)
	}
	if _, err = e.exchange(refreshForm(old)); err == nil {
		t.Fatal("expired old refresh accepted")
	}
	err = f.WithRequest(ctx, func(c context.Context) error {
		_, e := f.GetAccessTokenSession(c, e.strategy.AccessTokenSignature(c, next.GetAccessToken()), new(Session))
		return e
	})
	if err == nil {
		t.Fatal("retained client-bound old refresh replay after expiry does not revoke live family")
	}
}

func TestOAuthCleanupSupportsPhoneV1WithoutFamilyColumns(t *testing.T) {
	f := fixture(t, Active)
	for _, q := range []string{`CREATE TABLE mobile_device_streams(owner_id TEXT,device_id TEXT,epoch INTEGER)`, `CREATE TABLE mobile_native_sessions(owner_id TEXT,device_id TEXT,id TEXT,state TEXT)`} {
		if _, err := f.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.db.Exec(`UPDATE oauth_families SET expires_at=? WHERE id=?`, time.Now().Add(-8*24*time.Hour).Unix(), f.family.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.Sweep(ctx); err != nil {
		t.Fatal("chat-only cleanup requires device migration", err)
	}
	var n int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM oauth_families WHERE id=?`, f.family.ID).Scan(&n); err != nil || n != 0 {
		t.Fatal("unreferenced expired family retained", n, err)
	}
}

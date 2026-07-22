package slack

import (
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

// A fresh cached hit is returned without touching the (nil) Slack client — if the
// short-circuit failed, c.client.GetUserInfo would panic on the nil client.
func TestLookupUser_PositiveCacheHit(t *testing.T) {
	c := &Channel{userCache: make(map[string]userInfo), logger: zap.NewNop()}
	c.userCache["U1"] = userInfo{name: "Bob", lang: "en", ts: time.Now()}

	name, lang, ok := c.lookupUser("U1")
	if !ok || name != "Bob" || lang != "en" {
		t.Fatalf("lookupUser = (%q,%q,%v), want (Bob,en,true)", name, lang, ok)
	}
}

// A fresh cached failure returns a miss without re-calling GetUserInfo, so a
// burst from an unresolvable user cannot hammer the API (nil client would panic
// if the negative cache were ignored).
func TestLookupUser_NegativeCacheShortCircuits(t *testing.T) {
	c := &Channel{userCache: make(map[string]userInfo), logger: zap.NewNop()}
	c.userCache["U2"] = userInfo{failed: true, ts: time.Now()}

	if _, _, ok := c.lookupUser("U2"); ok {
		t.Fatal("expected miss for cached failure")
	}
}

func TestLookupUser_EmptyUser(t *testing.T) {
	c := &Channel{userCache: make(map[string]userInfo), logger: zap.NewNop()}
	if _, _, ok := c.lookupUser(""); ok {
		t.Fatal("expected miss for empty user id")
	}
}

// shutdownCh must unblock a select the way Ask relies on: closing it fires the
// case even with no value ever sent.
func TestShutdownCh_Unblocks(t *testing.T) {
	c := &Channel{shutdownCh: make(chan struct{})}
	var wg sync.WaitGroup
	wg.Add(1)
	done := make(chan struct{})
	go func() {
		defer wg.Done()
		select {
		case <-c.shutdownCh:
			close(done)
		case <-time.After(2 * time.Second):
			t.Error("shutdownCh did not unblock")
		}
	}()
	c.shutdownOnce.Do(func() { close(c.shutdownCh) })
	wg.Wait()
	select {
	case <-done:
	default:
		t.Fatal("expected done to be closed")
	}
}

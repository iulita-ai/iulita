package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	business "github.com/iulita-ai/iulita/internal/telegrambusiness"
)

func TestTelegramBusinessSourceBudgetScopesAndAtomicRollback(t *testing.T) {
	s, r, key, owner := businessStoreFixture(t)
	ctx := context.Background()
	if checkErr1 := r.Update(ctx, key, func(state *business.State) error {
		state.Connection.Policy.TokenOnly = true
		state.Connection.Policy.MediaConsent = true
		state.Connection.Policy.MediaProvider = "stt-fixture"
		for _, id := range []int64{303, 304, 305, 306} {
			policy := state.Peers[303].Policy
			policy.TokenOnly = true
			policy.MediaConsent = true
			policy.MediaProvider = "stt-fixture"
			state.Peers[id] = &business.Peer{ID: id, Policy: policy}
		}
		return nil
	}); checkErr1 != nil {
		t.Fatal(checkErr1)
	}
	now := time.Now().UTC()
	request := business.SourceBudgetRequest{Scope: business.Scope{Key: key, OwnerID: owner, PeerID: 303}, Bytes: 100, DurationSeconds: 120, At: now}
	for _, peer := range []int64{303, 304, 305} {
		request.Scope.PeerID = peer
		for i := 0; i < 5; i++ {
			if checkErr2 := r.ReserveSource(ctx, request); checkErr2 != nil {
				t.Fatalf("peer%d attempt%d: %v", peer, i, checkErr2)
			}
		}
		if checkErr3 := r.ReserveSource(ctx, request); !errors.Is(checkErr3, business.ErrQuota) {
			t.Fatalf("600 second peer cap bypassed: %v", checkErr3)
		}
	}
	request.Scope.PeerID = 306
	if checkErr4 := r.ReserveSource(ctx, request); !errors.Is(checkErr4, business.ErrQuota) {
		t.Fatalf("connection cap bypassed by new peer: %v", checkErr4)
	}
	var rows int
	if checkErr5 := s.db.NewRaw(`SELECT COUNT(*) FROM telegram_business_source_usage WHERE scope_key=?`, businessBudgetScopes(request.Scope)[0].Key).Scan(ctx, &rows); checkErr5 != nil || rows != 0 {
		t.Fatalf("failed late-scope admission charged peer: %d %v", rows, checkErr5)
	}
	request.At = now.Add(24 * time.Hour)
	if checkErr6 := r.ReserveSource(ctx, request); checkErr6 != nil {
		t.Fatalf("new UTC day failed: %v", checkErr6)
	}
	request.DurationSeconds = 121
	if checkErr7 := r.ReserveSource(ctx, request); !errors.Is(checkErr7, business.ErrDenied) {
		t.Fatalf("oversize duration: %v", checkErr7)
	}
	request.DurationSeconds = 120
	request.Bytes = business.MaxMediaBytes + 1
	if checkErr8 := r.ReserveSource(ctx, request); !errors.Is(checkErr8, business.ErrDenied) {
		t.Fatalf("oversize bytes: %v", checkErr8)
	}
}

func TestTelegramBusinessSourceBudgetRequiresBothConsentsAndPreservesDebits(t *testing.T) {
	s, r, key, owner := businessStoreFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	request := business.SourceBudgetRequest{Scope: business.Scope{Key: key, OwnerID: owner, PeerID: 303}, Bytes: 100, DurationSeconds: 120, At: now}
	if checkErr9 := r.ReserveSource(ctx, request); !errors.Is(checkErr9, business.ErrDenied) {
		t.Fatalf("unconsented STT admitted: %v", checkErr9)
	}
	if checkErr10 := r.Update(ctx, key, func(state *business.State) error {
		state.Connection.Policy.MediaConsent = true
		state.Connection.Policy.MediaProvider = "stt-fixture"
		state.Connection.Policy.TokenOnly = true
		state.Peers[303].Policy.MediaConsent = true
		state.Peers[303].Policy.MediaProvider = "stt-fixture"
		state.Peers[303].Policy.TokenOnly = true
		return nil
	}); checkErr10 != nil {
		t.Fatal(checkErr10)
	}
	if checkErr11 := r.ReserveSource(ctx, request); checkErr11 != nil {
		t.Fatal(checkErr11)
	}
	// Cancellation/result failure has no refund API. A later pause blocks new
	// calls but must not erase the numeric debit of the admitted attempt.
	if checkErr12 := r.Update(ctx, key, func(state *business.State) error { state.Connection.Paused = true; return nil }); checkErr12 != nil {
		t.Fatal(checkErr12)
	}
	if checkErr13 := r.ReserveSource(ctx, request); !errors.Is(checkErr13, business.ErrDenied) {
		t.Fatalf("paused STT admitted: %v", checkErr13)
	}
	var usage struct{ Calls, Seconds int64 }
	if checkErr14 := s.db.NewRaw(`SELECT calls,duration_seconds AS seconds FROM telegram_business_source_usage WHERE scope_key=? AND day=?`, businessBudgetScopes(request.Scope)[0].Key, now.Format("2006-01-02")).Scan(ctx, &usage); checkErr14 != nil || usage.Calls != 1 || usage.Seconds != 120 {
		t.Fatalf("failed/paused call refunded debit: %+v %v", usage, checkErr14)
	}
}

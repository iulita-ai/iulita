package telegrambusiness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestExpiredPreviewsDoNotPermanentlyExhaustPendingSlots(t *testing.T) {
	f := setup(t, false)
	clock := f.now
	f.service.options.Now = func() time.Time { return clock }
	in := PrepareInput{Scope: f.scope, Method: SendText, Params: Params{"text": json.RawMessage(`"Owner preview"`)}}
	for i := 0; i < 100; i++ {
		if _, e := f.service.Prepare(context.Background(), in); e != nil {
			t.Fatal(i, e)
		}
	}
	if _, e := f.service.Prepare(context.Background(), in); !errors.Is(e, ErrQuota) {
		t.Fatal("pending ceiling absent", e)
	}
	clock = clock.Add(5 * time.Minute)
	if _, e := f.service.Prepare(context.Background(), in); e != nil {
		t.Fatal("expired previews permanently blocked new intent", e)
	}
	st, _ := f.repo.Get(context.Background(), f.scope.Key)
	expired, pending := 0, 0
	for _, a := range st.Peers[99].Actions {
		switch a.State {
		case "expired":
			expired++
			if len(a.Params) > 0 {
				t.Fatal("expired private preview retained")
			}
		case "prepared":
			pending++
		}
	}
	if expired != 100 || pending != 1 {
		t.Fatal(expired, pending)
	}
}
func TestTerminalPreviewQuotaNeverTrimsUnknownOrSentLedger(t *testing.T) {
	f := setup(t, false)
	if e := f.repo.Update(context.Background(), f.scope.Key, func(st *State) error {
		p := initPeer(st, 99)
		for i := 0; i < 5000; i++ {
			id := fmt.Sprint(i)
			p.Actions[id] = &Action{ID: id, State: "expired", CreatedAt: f.now.Add(-time.Hour)}
		}
		p.Actions["uncertain"] = &Action{ID: "uncertain", State: "unknown", Automatic: true, ReservedStars: 10, CreatedAt: f.now}
		p.Actions["sent"] = &Action{ID: "sent", State: "sent", Automatic: true, ReservedStars: 20, CreatedAt: f.now}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if _, e := f.service.Prepare(context.Background(), PrepareInput{Scope: f.scope, Method: SendText, Params: Params{"text": json.RawMessage(`"Fresh preview"`)}}); e != nil {
		t.Fatal(e)
	}
	st, _ := f.repo.Get(context.Background(), f.scope.Key)
	p := st.Peers[99]
	if len(p.Actions) > 5000 || p.Actions["uncertain"] == nil || p.Actions["sent"] == nil || p.Actions["uncertain"].ReservedStars != 10 {
		t.Fatal("bounded trimming refunded terminal ledger")
	}
}

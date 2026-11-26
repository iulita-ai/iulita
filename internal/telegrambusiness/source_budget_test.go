package telegrambusiness

import (
	"context"
	"errors"
	"testing"
)

type sourceBudgetCounter struct {
	calls   int
	request SourceBudgetRequest
}

func (b *sourceBudgetCounter) ReserveSource(_ context.Context, request SourceBudgetRequest) error {
	b.calls++
	b.request = request
	return nil
}

func TestBusinessSourceBudgetHelperFailsClosedBeforeProvider(t *testing.T) {
	scope := Scope{Key: Key{InstanceID: "instance", BotID: 202, ConnectionID: "connection"}, OwnerID: "owner", PeerID: 303}
	budget := &sourceBudgetCounter{}
	if checkErr1 := ReserveSource(WithSourceBudget(context.Background(), budget), scope, 100, 120); !errors.Is(checkErr1, ErrDenied) {
		t.Fatalf("missing authority: %v", checkErr1)
	}
	guarded := WithSourceGuard(context.Background(), func(context.Context) error { return nil })
	if checkErr2 := ReserveSource(guarded, scope, 100, 120); !errors.Is(checkErr2, ErrUnavailable) {
		t.Fatalf("missing durable budget: %v", checkErr2)
	}
	ctx := WithSourceBudget(guarded, budget)
	if checkErr3 := ReserveSource(ctx, scope, 100, 121); !errors.Is(checkErr3, ErrDenied) || budget.calls != 0 {
		t.Fatalf("invalid duration admitted: %v", checkErr3)
	}
	if checkErr4 := ReserveSource(ctx, scope, 100, 120); checkErr4 != nil || budget.calls != 1 || budget.request.DurationSeconds != 120 || budget.request.At.IsZero() {
		t.Fatalf("numeric reservation missing: %v", checkErr4)
	}
}

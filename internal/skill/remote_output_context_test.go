package skill

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

func TestRemoteOutputBudgetIsSharedBoundedAndFailsClosed(t *testing.T) {
	if ConsumeRemoteOutputBudget(context.Background(), 1) != 0 {
		t.Fatal("missing turn state allowed output")
	}
	ctx := WithTurnTaint(context.Background())
	var total atomic.Int64
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() { defer wg.Done(); total.Add(int64(ConsumeRemoteOutputBudget(ctx, 4096))) }()
	}
	wg.Wait()
	if total.Load() != 64*1024 {
		t.Fatalf("budget: %d", total.Load())
	}
	if ConsumeRemoteOutputBudget(ctx, 1) != 0 {
		t.Fatal("budget expanded")
	}
	if RemoteOutputUsedInTurn(ctx) {
		t.Fatal("budget alone tainted turn")
	}
	MarkRemoteOutputUsed(ctx)
	if !RemoteOutputUsedInTurn(context.WithValue(ctx, ctxChatID, "chat")) {
		t.Fatal("taint lost in child context")
	}
}

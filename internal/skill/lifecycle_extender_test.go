package skill

import (
	"context"
	"testing"
	"time"
)

func TestLifecycleExtenderPreservesRootCancellation(t *testing.T) {
	root, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	// Message context: short deadline + values.
	msgCtx, msgCancel := context.WithTimeout(WithChatID(context.Background(), "c9"), 10*time.Millisecond)
	defer msgCancel()

	ext := LifecycleExtender(root)
	extended, extCancel := ext(msgCtx, 5*time.Second)
	defer extCancel()

	if got := ChatIDFrom(extended); got != "c9" {
		t.Fatalf("values lost: %q", got)
	}
	if err := extended.Err(); err != nil {
		t.Fatalf("extended already done: %v", err)
	}

	// The per-message deadline must NOT kill the extension...
	time.Sleep(20 * time.Millisecond)
	if err := extended.Err(); err != nil {
		t.Fatalf("message deadline leaked into extension: %v", err)
	}
	// ...but root shutdown must.
	rootCancel()
	deadline := time.After(500 * time.Millisecond)
	for extended.Err() == nil {
		select {
		case <-deadline:
			t.Fatal("root cancellation never propagated to extension")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestLifecycleExtenderNilRootMatchesDefault(t *testing.T) {
	msgCtx, msgCancel := context.WithTimeout(WithChatID(context.Background(), "c1"), 10*time.Millisecond)
	defer msgCancel()
	ext := LifecycleExtender(context.Background())
	extended, cancel := ext(msgCtx, time.Second)
	defer cancel()
	if ChatIDFrom(extended) != "c1" {
		t.Fatal("values lost")
	}
}

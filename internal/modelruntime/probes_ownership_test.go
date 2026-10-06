package modelruntime

import (
	"context"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2/utils"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/models"
)

func TestProbeRetainsProfileIdentityAfterRequestBufferReuse(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	factory := func(profile models.Profile, c Connection) (llm.Provider, error) {
		inner, err := echoFactory(profile, c)
		return providerFunc(func(ctx context.Context, req llm.Request) (llm.Response, error) {
			close(entered)
			select {
			case <-ctx.Done():
				return llm.Response{}, ctx.Err()
			case <-release:
				return inner.Complete(ctx, req)
			}
		}), err
	}
	m := newTestManager(t, &memoryRepo{}, factory)
	t.Cleanup(func() { _ = m.Close(context.Background()) })
	stage := stageDraft(t, m, 0, "synthetic-fixture-key")
	// Fiber returns route parameters as views of request-owned memory.
	urlBuffer := []byte("ds-flash")
	v, err := m.Probe(context.Background(), "admin", ProbeRequest{StageID: stage.ID,
		ProfileID: utils.UnsafeString(urlBuffer), Kind: "text", IdempotencyKey: probeKey()})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("probe did not start")
	}
	copy(urlBuffer, "recycled")
	close(release)
	finished := waitProbe(t, m, v.ID)
	if finished.ProfileID != "ds-flash" || finished.Result == nil || !finished.Result.Passed {
		t.Fatalf("probe identity or result changed: %+v", finished)
	}
	m.mu.Lock()
	evidence := m.state.Stage.Evidence["ds-flash"]["text"]
	m.mu.Unlock()
	if !evidence.Passed {
		t.Fatal("passed check was not credited to its resolved profile")
	}
}

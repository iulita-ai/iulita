package engine

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/mobile/conversations"
)

func TestSuccessfulEffectAfterDeadlineHasUnknownOutcome(t *testing.T) {
	root, expire := context.WithCancelCause(context.Background())
	defer expire(nil)
	var calls int
	p := providerFunc(func(context.Context, llm.Request) (llm.Response, error) {
		return llm.Response{ToolCalls: []llm.ToolCall{{ID: "x", Name: "mutation", Input: json.RawMessage(`{}`)}}}, nil
	})
	tool := Tool{Definition: llm.ToolDefinition{Name: "mutation"}, Revision: "1", Effect: "mutating", Isolated: true, Execute: func(ctx context.Context, f conversations.Fence, b json.RawMessage) (string, error) {
		calls++
		// Inject deadline cancellation only after StartTool committed. A short
		// wall-clock budget can instead expire during setup on a loaded runner,
		// which correctly reports failed because no effect has started.
		expire(context.DeadlineExceeded)
		<-ctx.Done()
		if !errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
			t.Fatal("execution did not observe deadline cancellation")
		}
		return "external effect committed", nil
	}}
	e, owner, c := engineFixture(t, p, []Tool{tool})
	e.Timeout = 5 * time.Second
	turn := accepted(t, e, owner, c.ID, "effect")
	runtime, err := e.Repo.AcquireRuntime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Repo.ReleaseRuntime(context.Background(), runtime)
	claimed, fence, err := e.Repo.Claim(context.Background(), runtime, owner)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.process(root, claimed, fence); err != nil {
		t.Fatal(err)
	}
	got, err := e.Repo.GetTurn(context.Background(), owner, c.ID, turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "unknown_outcome" || got.OutputMessageID != "" || calls != 1 {
		t.Fatalf("effect finished after deadline, journal finalization failed, turn reports %s", got.State)
	}
	if _, _, err = e.Repo.Claim(context.Background(), runtime, owner); !errors.Is(err, conversations.ErrNoTurn) {
		t.Fatal("uncertain effect became executable again", err)
	}
}

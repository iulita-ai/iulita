package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/mobile/conversations"
)

func TestSuccessfulEffectAfterDeadlineHasUnknownOutcome(t *testing.T) {
	p := providerFunc(func(context.Context, llm.Request) (llm.Response, error) {
		return llm.Response{ToolCalls: []llm.ToolCall{{ID: "x", Name: "mutation", Input: json.RawMessage(`{}`)}}}, nil
	})
	tool := Tool{Definition: llm.ToolDefinition{Name: "mutation"}, Revision: "1", Effect: "mutating", Isolated: true, Execute: func(ctx context.Context, f conversations.Fence, b json.RawMessage) (string, error) {
		<-ctx.Done()
		return "external effect committed", nil
	}}
	e, owner, c := engineFixture(t, p, []Tool{tool})
	e.Timeout = 100 * time.Millisecond
	turn := accepted(t, e, owner, c.ID, "effect")
	runOne(t, e, owner)
	got, err := e.Repo.GetTurn(context.Background(), owner, c.ID, turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "unknown_outcome" {
		t.Fatalf("effect finished after deadline, journal finalization failed, turn reports %s", got.State)
	}
}

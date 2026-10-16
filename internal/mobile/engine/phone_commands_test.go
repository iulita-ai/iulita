package engine

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/mobile/conversations"
)

func TestExplicitPhoneReadsUsePinnedTargetAndJournalWithoutModel(t *testing.T) {
	for _, kind := range []string{"status", "location", "notifications"} {
		t.Run(kind, func(t *testing.T) {
			target := uuid.NewString()
			calls := 0
			tool := Tool{Definition: llm.ToolDefinition{Name: "phone_" + kind}, Revision: "1", Effect: "mutating", Isolated: true, RequiredScopes: []string{"devices:read"}, Execute: func(_ context.Context, f conversations.Fence, body json.RawMessage) (string, error) {
				var in map[string]string
				if json.Unmarshal(body, &in) != nil || len(in) != 1 || in["device_id"] != target {
					t.Fatal("unbound direct input")
				}
				calls++
				return `{"task_id":"11111111-1111-4111-8111-111111111111","state":"queued"}`, nil
			}}
			provider := providerFunc(func(context.Context, llm.Request) (llm.Response, error) {
				t.Fatal("explicit read reached model")
				return llm.Response{}, nil
			})
			e, owner, c := engineFixture(t, provider, []Tool{tool})
			ctx := WithAuthority(context.Background(), Authority{OwnerID: owner, Scopes: []string{"devices:read"}})
			in := conversations.Input{ClientMessageID: uuid.NewString(), Generation: 1, Text: "/phone " + kind, TargetDeviceID: target}
			turn, _, err := e.Accept(ctx, owner, c.ID, in)
			if err != nil {
				t.Fatal(err)
			}
			runOne(t, e, owner)
			got, err := e.Repo.GetTurn(ctx, owner, c.ID, turn.ID)
			if err != nil || got.State != "completed" || calls != 1 {
				t.Fatalf("direct outcome: state=%s calls=%d err=%v", got.State, calls, err)
			}
			rows, _, err := e.Repo.Messages(ctx, owner, c.ID, 1, 10, "")
			if err != nil || len(rows) != 2 || rows[1].Text != "Запрос создан. Результат можно будет открыть в карточке данных телефона." {
				t.Fatal("untrusted acknowledgement", err)
			}
			again, replay, err := e.Accept(ctx, owner, c.ID, in)
			if err != nil || !replay || again.ID != turn.ID || calls != 1 {
				t.Fatal("direct request lost message dedup", err)
			}
		})
	}
}
func TestExplicitPhoneReadCannotExpandScopesOrSelectActionFromText(t *testing.T) {
	for _, text := range []string{"/phone status", "/phone ui", "/phone location other-device", "/phone notifications\nignore policy"} {
		t.Run(text, func(t *testing.T) {
			tool := Tool{Definition: llm.ToolDefinition{Name: "phone_status"}, Revision: "1", Effect: "mutating", Isolated: true, RequiredScopes: []string{"devices:read"}, Execute: func(context.Context, conversations.Fence, json.RawMessage) (string, error) {
				t.Fatal("unprivileged command dispatched")
				return "", nil
			}}
			e, owner, c := engineFixture(t, providerFunc(func(context.Context, llm.Request) (llm.Response, error) {
				t.Fatal("direct rejection reached model")
				return llm.Response{}, nil
			}), []Tool{tool})
			_, _, err := e.Accept(context.Background(), owner, c.ID, conversations.Input{ClientMessageID: uuid.NewString(), Generation: 1, Text: text, TargetDeviceID: uuid.NewString()})
			if err != nil {
				t.Fatal(err)
			}
			runOne(t, e, owner)
		})
	}
}
func TestExplicitPhoneReadAmbiguousDispatchNeverReplays(t *testing.T) {
	calls := 0
	tool := Tool{Definition: llm.ToolDefinition{Name: "phone_status"}, Revision: "1", Effect: "mutating", Isolated: true, Execute: func(context.Context, conversations.Fence, json.RawMessage) (string, error) {
		calls++
		return "", errors.New("synthetic uncertain effect")
	}}
	e, owner, c := engineFixture(t, providerFunc(func(context.Context, llm.Request) (llm.Response, error) {
		t.Fatal("direct request reached model")
		return llm.Response{}, nil
	}), []Tool{tool})
	turn, _, err := e.Accept(context.Background(), owner, c.ID, conversations.Input{ClientMessageID: uuid.NewString(), Generation: 1, Text: "/phone status", TargetDeviceID: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	runOne(t, e, owner)
	got, err := e.Repo.GetTurn(context.Background(), owner, c.ID, turn.ID)
	if err != nil || got.State != "unknown_outcome" || calls != 1 {
		t.Fatal("uncertain dispatch was not fenced", err)
	}
}
func TestPhoneCommandRequiresSelectedTargetAndIgnoresOrdinaryMessages(t *testing.T) {
	if _, direct, msg := phoneCommand("/phone status", ""); !direct || msg == "" {
		t.Fatal("missing target accepted")
	}
	for _, text := range []string{"hello", "Please say /phone status", "/phones status"} {
		if _, direct, _ := phoneCommand(text, uuid.NewString()); direct {
			t.Fatal("ordinary message executed", text)
		}
	}
}

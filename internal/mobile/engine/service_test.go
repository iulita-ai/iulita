package engine

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/mobile/conversations"
)

func TestServiceAuthorityFencesLateModelCompletionWithoutDeviceFamily(t *testing.T) {
	var revoked atomic.Bool
	provider := providerFunc(func(context.Context, llm.Request) (llm.Response, error) {
		revoked.Store(true)
		return llm.Response{Content: "late-service-canary"}, nil
	})
	processor, owner, conversation := engineFixture(t, provider, nil)
	authority := Authority{PrincipalType: "service", OwnerID: owner, ServiceID: uuid.NewString(), ServiceGrantID: uuid.NewString(), Scopes: []string{"chats:write"}}
	processor.VerifyAuthority = func(_ context.Context, actual Authority) error {
		if actual.PrincipalType != "service" || actual.ServiceGrantID != authority.ServiceGrantID || actual.FamilyID != "" || revoked.Load() {
			return errors.New("service access revoked")
		}
		return nil
	}
	turn, _, err := processor.Accept(WithAuthority(context.Background(), authority), owner, conversation.ID, conversations.Input{ClientMessageID: uuid.NewString(), Generation: 1, Text: "request"})
	if err != nil {
		t.Fatal(err)
	}
	runOne(t, processor, owner)
	result, err := processor.Repo.GetTurn(context.Background(), owner, conversation.ID, turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.State == "completed" || result.OutputMessageID != "" {
		t.Fatal("revoked service persisted provider output", result)
	}
}
func TestServiceCannotAdmitAccountMemoryConversation(t *testing.T) {
	processor, owner, _ := engineFixture(t, providerFunc(func(context.Context, llm.Request) (llm.Response, error) {
		t.Fatal("forbidden model call")
		return llm.Response{}, nil
	}), nil)
	processor.AccountMemory = savedMemoryFunc(func(context.Context, string) (string, error) { t.Fatal("service read account memory"); return "", nil })
	conversation, err := processor.Repo.Create(context.Background(), owner, "account", conversations.Settings{ProfileID: "default", MemoryMode: "account"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = processor.Accept(WithAuthority(context.Background(), Authority{PrincipalType: "service", OwnerID: owner}), owner, conversation.ID, conversations.Input{ClientMessageID: uuid.NewString(), Generation: 1, Text: "request"})
	if !errors.Is(err, conversations.ErrInvalid) {
		t.Fatal("service admitted account memory", err)
	}
}

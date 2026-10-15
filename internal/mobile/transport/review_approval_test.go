package transport

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/mobile/conversations"
	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestConfirmationRPCKeepsScopeAndApproverFamilyBinding(t *testing.T) {
	h := setupHarness(t)
	requester, sourceID := h.login(t, "owner")
	approver, _ := h.login(t, "owner")
	chatOnly, _ := h.login(t, "owner", "chats:read", "chats:write")
	chat := v1.NewChatServiceClient(h.conn)
	ctx := tokenContext(requester)
	c, err := chat.CreateConversation(ctx, &v1.CreateConversationRequest{Client: clientContext(), Operation: operation(), Title: "sensitive request", Settings: &v1.ConversationSettings{ProfileId: "default", MemoryMode: v1.MemoryMode_MEMORY_MODE_ISOLATED}})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := chat.SendMessage(ctx, &v1.SendMessageRequest{Client: clientContext(), Operation: operation(), ConversationId: c.ConversationId, ClientMessageId: uuid.NewString(), ExpectedHistoryGeneration: 1, Text: "phone-torch:" + sourceID})
	if err != nil {
		t.Fatal(err)
	}
	var prompt *v1.Prompt
	deadline := time.Now().Add(3 * time.Second)
	for {
		snap, stepErr1 := chat.GetSnapshot(ctx, &v1.GetSnapshotRequest{Client: clientContext(), Subscription: &v1.Subscription{Filter: &v1.Subscription_Selected{Selected: &v1.ConversationIDs{ConversationIds: []string{c.ConversationId}}}}})
		if stepErr1 != nil {
			t.Fatal(stepErr1)
		}
		if len(snap.Conversations) > 0 && len(snap.Conversations[0].PendingPrompts) > 0 {
			prompt = snap.Conversations[0].PendingPrompts[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("sensitive tool did not open a prompt")
		}
		time.Sleep(15 * time.Millisecond)
	}
	confirmation := prompt.GetConfirmation()
	request := &v1.RespondToConfirmationRequest{Client: clientContext(), Operation: operation(), Binding: prompt.Binding, SecurityOperationId: confirmation.OperationId, Nonce: confirmation.Nonce, FingerprintSha256: confirmation.FingerprintSha256, Decision: v1.Decision_DECISION_APPROVE}
	if _, err = chat.RespondToConfirmation(tokenContext(chatOnly), request); status.Code(err) != codes.PermissionDenied {
		t.Fatal("chats:write approved system permission", err)
	}
	if _, err = chat.RespondToConfirmation(tokenContext(approver), request); err != nil {
		t.Fatal("authorized approver denied", err)
	}
	persisted, err := h.chats.Prompt(context.Background(), h.owners["owner"], c.ConversationId, turn.TurnId, prompt.Binding.PromptId)
	if err != nil || persisted.Approver == nil {
		t.Fatal("approval evidence missing", err)
	}
	f := conversations.Fence{OwnerID: h.owners["owner"], ConversationID: c.ConversationId, TurnID: turn.TurnId, Generation: 1, ProcessingEpoch: persisted.ProcessingEpoch}
	verify := func(a conversations.ApprovalAuthority) error {
		return h.oauth.ValidateAuthority(context.Background(), oauthstore.Authority(a))
	}
	if err = h.chats.PhoneConsent(context.Background(), f, confirmation.OperationId, confirmation.FingerprintSha256, verify); err != nil {
		t.Fatal("live family approval denied", err)
	}
	if err = h.oauth.RevokeFamily(context.Background(), persisted.Approver.OwnerID, persisted.Approver.FamilyID); err != nil {
		t.Fatal(err)
	}
	if err = h.chats.PhoneConsent(context.Background(), f, confirmation.OperationId, confirmation.FingerprintSha256, verify); !errors.Is(err, conversations.ErrFenced) {
		t.Fatal("revoked approver retained task authority", err)
	}
}

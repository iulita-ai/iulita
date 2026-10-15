package transport

import (
	"context"
	"testing"

	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The login fixture generates software P-256 keys and completes real browser
// PKCE and installation activation, without Android APIs or attestation.
func TestPlatformNeutralChatClientsKeepPhoneScopesClosed(t *testing.T) {
	h := setupHarness(t)
	for _, kind := range []string{"console", "delegated_server"} {
		t.Run(kind, func(t *testing.T) {
			token, _ := h.login(t, "owner", "chats:read", "chats:write")
			claims, err := h.oauth.ValidateAccess(context.Background(), token)
			if err != nil || claims.Scope != "chats:read chats:write" {
				t.Fatal("unexpected installation scopes", claims, err)
			}
			ctx := tokenContext(token)
			conversation, turn := origin(t, h, token, "hello from "+kind)
			chat := v1.NewChatServiceClient(h.conn)
			if _, conversationErr := chat.GetConversation(ctx, &v1.GetConversationRequest{Client: clientContext(), ConversationId: conversation}); conversationErr != nil {
				t.Fatal(conversationErr)
			}
			if _, turnErr := chat.GetTurn(ctx, &v1.GetTurnRequest{Client: clientContext(), ConversationId: conversation, Lookup: &v1.GetTurnRequest_TurnId{TurnId: turn}}); turnErr != nil {
				t.Fatal(turnErr)
			}
			device := v1.NewDeviceServiceClient(h.conn)
			if _, resultErr := device.GetTaskResult(ctx, &v1.GetTaskResultRequest{Client: clientContext()}); status.Code(resultErr) != codes.PermissionDenied {
				t.Fatal("chat-only installation accessed phone result boundary", resultErr)
			}
			phone := v1.NewPhoneServiceClient(h.conn)
			if _, prepareErr := phone.PrepareProcessing(ctx, &v1.PrepareProcessingRequest{Client: clientContext(), Operation: operation()}); status.Code(prepareErr) != codes.PermissionDenied {
				t.Fatal("chat-only installation accessed private provider boundary", prepareErr)
			}
			if _, bindingErr := phone.BindExternalOrigin(ctx, &v1.BindExternalOriginRequest{Client: clientContext(), Operation: operation()}); status.Code(bindingErr) != codes.PermissionDenied {
				t.Fatal("chat-only installation accessed phone management boundary", bindingErr)
			}
			stream, err := device.Control(ctx)
			if err != nil {
				t.Fatal(err)
			}
			// The gRPC server can reject a stream before the first Send reaches it.
			_ = stream.Send(&v1.ClientControlFrame{ProtocolVersion: 1, Body: &v1.ClientControlFrame_Hello{Hello: &v1.ControlHello{Client: clientContext()}}})
			if _, err := stream.Recv(); status.Code(err) != codes.PermissionDenied {
				t.Fatal("chat-only installation opened native Control", err)
			}
		})
	}
}

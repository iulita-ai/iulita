package transport

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (h *harness) serviceToken(t *testing.T, id, secret, scope string) string {
	t.Helper()
	form := url.Values{"grant_type": {"client_credentials"}}
	if scope != "" {
		form.Set("scope", scope)
	}
	request := httptest.NewRequest("POST", harnessIssuer+"/oauth2/token", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.SetBasicAuth(id, secret)
	response := httptest.NewRecorder()
	h.browser.ServeHTTP(response, request)
	var token oauthstore.TokenReply
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &token) != nil || token.AccessToken == "" || token.RefreshToken != "" {
		t.Fatalf("service HTTP %d %s", response.Code, response.Body.String())
	}
	return token.AccessToken
}
func TestServiceClientTLSGRPCLeastPrivilege(t *testing.T) {
	h := setupHarness(t)
	ctx := context.Background()
	secret, _ := oauthstore.Secret()
	if err := h.oauth.RegisterOAuthClient(ctx, oauthstore.ClientRegistration{ClientID: "reader", Name: "reader", Kind: "service", OwnerID: h.owners["owner"]}, secret); err != nil {
		t.Fatal(err)
	}
	token := h.serviceToken(t, "reader", secret, "")
	client := v1.NewChatServiceClient(h.conn)
	if _, err := client.ListConversations(tokenContext(token), &v1.ListConversationsRequest{Client: clientContext()}); err != nil {
		t.Fatal("service read", err)
	}
	create := &v1.CreateConversationRequest{Client: clientContext(), Operation: operation(), Title: "service chat", Settings: &v1.ConversationSettings{ProfileId: "default", MemoryMode: v1.MemoryMode_MEMORY_MODE_ISOLATED}}
	if _, err := client.CreateConversation(tokenContext(token), create); status.Code(err) != codes.PermissionDenied {
		t.Fatal("reader wrote chat", err)
	}
	if _, err := v1.NewDeviceServiceClient(h.conn).ListDevices(tokenContext(token), &v1.ListDevicesRequest{Client: clientContext()}); status.Code(err) != codes.PermissionDenied {
		t.Fatal("service accessed phones", err)
	}
	writerSecret, _ := oauthstore.Secret()
	if err := h.oauth.RegisterOAuthClient(ctx, oauthstore.ClientRegistration{ClientID: "writer", Name: "writer", Kind: "service", OwnerID: h.owners["owner"], Scopes: []string{"chats:read", "chats:write"}}, writerSecret); err != nil {
		t.Fatal(err)
	}
	writer := h.serviceToken(t, "writer", writerSecret, "chats:read chats:write")
	if _, factErr := v1.NewChatExtrasServiceClient(h.conn).SaveAnswerFact(tokenContext(writer), &v1.SaveAnswerFactRequest{Client: clientContext(), Operation: operation(), ConfirmAccountExport: true}); status.Code(factErr) != codes.PermissionDenied {
		t.Fatal("service principal exported account fact", factErr)
	}

	if _, promptErr := client.RespondToConfirmation(tokenContext(writer), &v1.RespondToConfirmationRequest{Client: clientContext()}); status.Code(promptErr) != codes.PermissionDenied {
		t.Fatal("service supplied interactive approval", promptErr)
	}
	conversation, err := client.CreateConversation(tokenContext(writer), create)
	if err != nil {
		t.Fatal("service write", err)
	}
	create.Operation = operation()
	create.Settings.MemoryMode = v1.MemoryMode_MEMORY_MODE_ACCOUNT
	if _, err = client.CreateConversation(tokenContext(writer), create); status.Code(err) != codes.InvalidArgument {
		t.Fatal("service selected account memory", err)
	}
	if _, err = client.SendMessage(tokenContext(writer), &v1.SendMessageRequest{Client: clientContext(), Operation: operation(), ConversationId: conversation.ConversationId, ClientMessageId: uuid.NewString(), ExpectedHistoryGeneration: 1, Text: "server message"}); err != nil {
		t.Fatal("service send", err)
	}
	foreignSecret, _ := oauthstore.Secret()
	if err = h.oauth.RegisterOAuthClient(ctx, oauthstore.ClientRegistration{ClientID: "foreign", Name: "foreign", Kind: "service", OwnerID: h.owners["other"]}, foreignSecret); err != nil {
		t.Fatal(err)
	}
	foreign := h.serviceToken(t, "foreign", foreignSecret, "")
	if _, err = client.GetConversation(tokenContext(foreign), &v1.GetConversationRequest{Client: clientContext(), ConversationId: conversation.ConversationId}); status.Code(err) != codes.NotFound {
		t.Fatal("service crossed owners", err)
	}
	if err = h.oauth.ChangeOAuthClient(ctx, "writer", "", true); err != nil {
		t.Fatal(err)
	}
	if _, err = client.ListConversations(tokenContext(writer), &v1.ListConversationsRequest{Client: clientContext()}); status.Code(err) != codes.Unauthenticated {
		t.Fatal("disabled service access", err)
	}
	user, err := h.db.GetUser(ctx, h.owners["owner"])
	if err != nil {
		t.Fatal(err)
	}
	user.SecurityEpoch++
	if err = h.db.UpdateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if _, err = client.ListConversations(tokenContext(token), &v1.ListConversationsRequest{Client: clientContext()}); status.Code(err) != codes.Unauthenticated {
		t.Fatal("account security epoch retained service access", err)
	}
}
func TestCLIDeviceFlowTLSGRPCChatAndPhoneBoundaries(t *testing.T) {
	h := setupHarness(t)
	ctx := context.Background()
	if err := h.oauth.RegisterOAuthClient(ctx, oauthstore.ClientRegistration{ClientID: "cli", Name: "Personal console", Kind: "cli"}, ""); err != nil {
		t.Fatal(err)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	jwk := oauthstore.InstallationJWK{Kty: "EC", Crv: "P-256", X: base64.RawURLEncoding.EncodeToString(key.X.FillBytes(make([]byte, 32))), Y: base64.RawURLEncoding.EncodeToString(key.Y.FillBytes(make([]byte, 32)))}
	response := h.http("/oauth2/device_authorization", url.Values{"client_id": {"cli"}, "iulita_installation_key_thumbprint": {jwk.Thumbprint()}}, nil)
	var device oauthstore.DeviceAuthorization
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &device) != nil {
		t.Fatal("CLI start", response.Body.String())
	}
	response = h.http("/oauth2/device?user_code="+url.QueryEscape(device.UserCode), nil, nil)
	form := fields(response)
	cookie := response.Result().Cookies()[0]
	form.Set("username", "owner")
	form.Set("password", "fixture password")
	response = h.http("/oauth2/login", form, cookie)
	form = fields(response)
	cookie = response.Result().Cookies()[0]
	form.Set("decision", "approve")
	form.Set("user_code", device.UserCode)
	if response = h.http("/oauth2/consent", form, cookie); response.Code != 200 {
		t.Fatal("CLI consent", response.Body.String())
	}
	pending := h.tokens(t, url.Values{"grant_type": {oauthstore.DeviceGrant}, "client_id": {"cli"}, "device_code": {device.DeviceCode}, "iulita_installation_jwk": {jwk.Canonical()}})
	if pending.Challenge == nil {
		t.Fatal("CLI proof challenge missing")
	}
	input, _ := base64.RawURLEncoding.DecodeString(pending.Challenge.SigningInput)
	sum := sha256.Sum256(input)
	signature, _ := ecdsa.SignASN1(rand.Reader, key, sum[:])
	x, _ := base64.RawURLEncoding.DecodeString(jwk.X)
	y, _ := base64.RawURLEncoding.DecodeString(jwk.Y)
	_, err := v1.NewDeviceServiceClient(h.conn).ActivateDevice(tokenContext(pending.Access), &v1.ActivateDeviceRequest{Client: clientContext(), Operation: &v1.Operation{OperationId: pending.Challenge.OperationID, IssuedAt: timestamppb.Now()}, PublicKey: &v1.InstallationPublicKey{P256X: x, P256Y: y}, Proof: &v1.InstallationProof{Nonce: pending.Challenge.Nonce, SignatureDer: signature}, Label: "Console"})
	if err != nil {
		t.Fatal("CLI installation proof", err)
	}
	active := h.tokens(t, url.Values{"grant_type": {"refresh_token"}, "client_id": {"cli"}, "refresh_token": {pending.Refresh}})
	client := v1.NewChatServiceClient(h.conn)
	conversation, err := client.CreateConversation(tokenContext(active.Access), &v1.CreateConversationRequest{Client: clientContext(), Operation: operation(), Title: "CLI chat", Settings: &v1.ConversationSettings{ProfileId: "default", MemoryMode: v1.MemoryMode_MEMORY_MODE_ISOLATED}})
	if err != nil {
		t.Fatal("CLI gRPC chat", err)
	}
	turn, err := client.SendMessage(tokenContext(active.Access), &v1.SendMessageRequest{Client: clientContext(), Operation: operation(), ConversationId: conversation.ConversationId, ClientMessageId: uuid.NewString(), ExpectedHistoryGeneration: 1, Text: "console message"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		got, getErr := client.GetTurn(tokenContext(active.Access), &v1.GetTurnRequest{Client: clientContext(), ConversationId: conversation.ConversationId, Lookup: &v1.GetTurnRequest_TurnId{TurnId: turn.TurnId}})
		if getErr != nil {
			t.Fatal(getErr)
		}
		if got.State == v1.TurnState_TURN_STATE_COMPLETED {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("CLI turn stalled", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err = v1.NewDeviceServiceClient(h.conn).ListDevices(tokenContext(active.Access), &v1.ListDevicesRequest{Client: clientContext()}); status.Code(err) != codes.PermissionDenied {
		t.Fatal("CLI default phone access", err)
	}
	if err = h.oauth.ChangeOAuthClient(ctx, "cli", "", true); err != nil {
		t.Fatal(err)
	}
	if _, err = client.GetConversation(tokenContext(active.Access), &v1.GetConversationRequest{Client: clientContext(), ConversationId: conversation.ConversationId}); status.Code(err) != codes.Unauthenticated {
		t.Fatal("CLI disabled access", err)
	}
}

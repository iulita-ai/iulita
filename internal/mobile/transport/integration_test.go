package transport

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/mobile/browseroauth"
	"github.com/iulita-ai/iulita/internal/mobile/conversations"
	"github.com/iulita-ai/iulita/internal/mobile/devices"
	"github.com/iulita-ai/iulita/internal/mobile/engine"
	"github.com/iulita-ai/iulita/internal/mobile/ephemeral"
	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
	legacy "github.com/iulita-ai/iulita/internal/storage/sqlite"
	"github.com/ory/fosite"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const harnessIssuer = "https://mobile.example.test"
const harnessRedirect = harnessIssuer + "/mobile/callback"
const harnessVerifier = "random-test-only-PKCE-verifier-12345678901234567890"

var harnessScopes = []string{"chats:read", "chats:write", "devices:read", "devices:manage", "devices:receive", "devices:sync", "phone:data", "phone:ui"}
var browserFields = regexp.MustCompile(`name="(flow_id|csrf)" value="([A-Za-z0-9_-]+)"`)

type echoProvider struct{}

func (echoProvider) Complete(ctx context.Context, r llm.Request) (llm.Response, error) {
	if strings.HasPrefix(r.Message, "phone-torch:") && len(r.ToolExchanges) == 0 {
		input, _ := json.Marshal(map[string]any{"device_id": strings.TrimPrefix(r.Message, "phone-torch:"), "action": "torch", "enabled": true})
		return llm.Response{ToolCalls: []llm.ToolCall{{ID: "torch-call", Name: "phone_action", Input: input}}}, nil
	}

	if (strings.HasPrefix(r.Message, "phone-status:") || strings.HasPrefix(r.Message, "Request phone_status for device_id ")) && len(r.ToolExchanges) == 0 {
		selected := strings.TrimPrefix(r.Message, "phone-status:")
		if strings.HasPrefix(r.Message, "Request phone_status for device_id ") {
			selected = strings.Fields(strings.TrimPrefix(r.Message, "Request phone_status for device_id "))[0]
			selected = strings.TrimSuffix(selected, ".")
		}
		input, _ := json.Marshal(map[string]string{"device_id": selected})
		return llm.Response{ToolCalls: []llm.ToolCall{{ID: "phone-call", Name: "phone_status", Input: input}}}, nil
	}
	return llm.Response{Content: "answer: " + r.Message}, nil
}

type harness struct {
	server  *Server
	db      *legacy.Store
	oauth   *oauthstore.Store
	browser http.Handler
	chats   *conversations.Repository
	conn    *grpc.ClientConn
	cancel  context.CancelFunc
	run     chan error
	owners  map[string]string
	devices *devices.Store
	paths   []string
}

func certificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "mobile.example.test"}, DNSNames: []string{"mobile.example.test"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}
func setupHarness(t *testing.T) *harness { return setupHarnessWithOptions(t, devices.Options{}) }
func setupHarnessWithOptions(t *testing.T, options devices.Options) *harness {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "main.sqlite")
	db, err := legacy.New(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.RunMigrations(ctx); err != nil {
		t.Fatal(err)
	}
	owners := map[string]string{}
	hash, _ := bcrypt.GenerateFromPassword([]byte("fixture password"), bcrypt.MinCost)
	for _, username := range []string{"owner", "other"} {
		id := uuid.NewString()
		owners[username] = id
		if err = db.CreateUser(ctx, &domain.User{ID: id, Username: username, PasswordHash: string(hash), DisplayName: username}); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(t.TempDir(), "realm")
	if err = os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	realm, err := oauthstore.InitializeRealm(filepath.Join(dir, "keys"), harnessIssuer, "iulita-mobile")
	if err != nil {
		t.Fatal(err)
	}
	oauth, err := oauthstore.Initialize(ctx, filepath.Join(dir, "oauth.sqlite"), realm)
	if err != nil {
		t.Fatal(err)
	}
	if err = oauth.RegisterClient(ctx, fosite.DefaultClient{ID: "mobile", Public: true, RedirectURIs: []string{harnessRedirect}, GrantTypes: []string{"authorization_code", "refresh_token"}, ResponseTypes: []string{"code"}, Scopes: harnessScopes, Audience: []string{"iulita-mobile"}}); err != nil {
		t.Fatal(err)
	}
	signer, _ := realm.CursorSigner()
	chats, err := conversations.OpenExisting(ctx, path, signer)
	if err != nil {
		t.Fatal(err)
	}
	accounts := browseroauth.PasswordAccounts{Users: db}
	content, err := ephemeral.New()
	if err != nil {
		t.Fatal(err)
	}
	deviceStore, err := devices.Open(ctx, filepath.Join(dir, "oauth.sqlite"), oauth, accounts, chats, content, options)
	if err != nil {
		t.Fatal(err)
	}
	processor, err := engine.New(chats, func(context.Context, string, string) (engine.Profile, error) {
		return engine.Profile{Provider: echoProvider{}, Revision: 1, Digest: "fixture-model"}, nil
	}, "personal chat", deviceStore.Tools())
	if err != nil {
		t.Fatal(err)
	}
	processor.VerifyAuthority = oauth.ValidateAuthority
	browser, err := browseroauth.New(browseroauth.Config{Clock: options.Clock, Store: oauth, Accounts: accounts, Scopes: harnessScopes, Limits: browseroauth.Limits{AuthorizePerIP: 100, LoginPerIP: 100, LoginPerAccount: 100, ConsentPerIP: 100}})
	if err != nil {
		t.Fatal(err)
	}
	cert, pool := certificate(t)
	server, err := New(Config{Clock: options.Clock, TLS: &tls.Config{Certificates: []tls.Certificate{cert}}, OAuth: oauth, Accounts: accounts, Chats: chats, Engine: processor, Devices: deviceStore, Features: config.MobileConfig{ChatEnabled: true, DeviceEnabled: true, UIEnabled: options.UI, VisionEnabled: options.Vision}, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve(listener)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: pool, ServerName: "mobile.example.test", MinVersion: tls.VersionTLS13})))
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	run := make(chan error, 1)
	go func() { run <- processor.Run(runCtx) }()
	contentDone := make(chan struct{})
	go func() { content.Run(runCtx); close(contentDone) }()
	deviceDone := make(chan error, 1)
	go func() { deviceDone <- deviceStore.Run(runCtx) }()
	h := &harness{server: server, db: db, oauth: oauth, browser: browser.Handler(), chats: chats, conn: conn, cancel: cancel, run: run, owners: owners, devices: deviceStore, paths: []string{path, filepath.Join(dir, "oauth.sqlite")}}
	t.Cleanup(func() {
		cancel()
		<-contentDone
		<-deviceDone
		select {
		case <-run:
		case <-time.After(5 * time.Second):
			t.Error("mobile worker did not stop")
		}
		shutdown, c := context.WithTimeout(context.Background(), time.Second)
		defer c()
		server.Shutdown(shutdown)
		conn.Close()
		chats.Close()
		deviceStore.Close()
		oauth.Close()
		db.Close()
	})
	return h
}
func (h *harness) http(path string, form url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	method := "GET"
	if form != nil {
		method = "POST"
	}
	r := httptest.NewRequest(method, harnessIssuer+path, strings.NewReader(form.Encode()))
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", harnessIssuer)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.browser.ServeHTTP(w, r)
	return w
}
func fields(w *httptest.ResponseRecorder) url.Values {
	v := url.Values{}
	for _, m := range browserFields.FindAllStringSubmatch(w.Body.String(), -1) {
		v.Set(m[1], m[2])
	}
	return v
}

type credentialsReply struct {
	Access    string                `json:"access_token"`
	Refresh   string                `json:"refresh_token"`
	Challenge *oauthstore.Challenge `json:"iulita_challenge"`
}

func (h *harness) tokens(t *testing.T, form url.Values) credentialsReply {
	t.Helper()
	w := h.http("/oauth2/token", form, nil)
	var tokens credentialsReply
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &tokens) != nil {
		t.Fatalf("token HTTP %d %s", w.Code, w.Body.String())
	}
	return tokens
}
func clientContext() *v1.ClientContext {
	return &v1.ClientContext{ProtocolVersion: 1, AppVersion: "test"}
}
func operation() *v1.Operation {
	return &v1.Operation{OperationId: uuid.NewString(), IssuedAt: timestamppb.Now()}
}
func tokenContext(token string) context.Context {
	return metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
}
func (h *harness) login(t *testing.T, username string, requestedScopes ...string) (accessToken, refreshToken string) {
	t.Helper()
	if len(requestedScopes) == 0 {
		requestedScopes = harnessScopes
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	jwk := oauthstore.InstallationJWK{Crv: "P-256", Kty: "EC", X: base64.RawURLEncoding.EncodeToString(key.X.FillBytes(make([]byte, 32))), Y: base64.RawURLEncoding.EncodeToString(key.Y.FillBytes(make([]byte, 32)))}
	challenge := sha256.Sum256([]byte(harnessVerifier))
	query := url.Values{"response_type": {"code"}, "client_id": {"mobile"}, "redirect_uri": {harnessRedirect}, "scope": {strings.Join(requestedScopes, " ")}, "state": {"random-state-fixture-at-least-128-bits"}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "iulita_installation_key_thumbprint": {jwk.Thumbprint()}}
	w := h.http("/oauth2/authorize?"+query.Encode(), nil, nil)
	form := fields(w)
	if w.Code != 200 || form.Get("csrf") == "" {
		t.Fatal("authorize form", w.Body.String())
	}
	cookie := w.Result().Cookies()[0]
	form.Set("username", username)
	form.Set("password", "fixture password")
	w = h.http("/oauth2/login", form, cookie)
	form = fields(w)
	if len(w.Result().Cookies()) == 0 {
		t.Fatal("login", w.Body.String())
	}
	cookie = w.Result().Cookies()[0]
	form.Set("decision", "approve")
	w = h.http("/oauth2/consent", form, cookie)
	if w.Code != 303 {
		t.Fatal("consent", w.Body.String())
	}
	callback, _ := url.Parse(w.Header().Get("Location"))
	pending := h.tokens(t, url.Values{"grant_type": {"authorization_code"}, "client_id": {"mobile"}, "redirect_uri": {harnessRedirect}, "code": {callback.Query().Get("code")}, "code_verifier": {harnessVerifier}, "iulita_installation_jwk": {jwk.Canonical()}})
	if _, err := v1.NewAccountServiceClient(h.conn).GetProfile(tokenContext(pending.Access), &v1.GetProfileRequest{Client: clientContext()}); status.Code(err) != codes.PermissionDenied {
		t.Fatal("pending accessed profile", err)
	}
	bytes, _ := base64.RawURLEncoding.DecodeString(pending.Challenge.SigningInput)
	sum := sha256.Sum256(bytes)
	signature, _ := ecdsa.SignASN1(rand.Reader, key, sum[:])
	x, _ := base64.RawURLEncoding.DecodeString(jwk.X)
	y, _ := base64.RawURLEncoding.DecodeString(jwk.Y)
	device, err := v1.NewDeviceServiceClient(h.conn).ActivateDevice(tokenContext(pending.Access), &v1.ActivateDeviceRequest{Client: clientContext(), Operation: &v1.Operation{OperationId: pending.Challenge.OperationID, IssuedAt: timestamppb.Now()}, PublicKey: &v1.InstallationPublicKey{P256X: x, P256Y: y}, Proof: &v1.InstallationProof{Nonce: pending.Challenge.Nonce, SignatureDer: signature}, Label: "test Pixel"})
	if err != nil {
		t.Fatal("gRPC activation", err)
	}
	active := h.tokens(t, url.Values{"grant_type": {"refresh_token"}, "client_id": {"mobile"}, "refresh_token": {pending.Refresh}})
	return active.Access, device.DeviceId
}
func TestTLSGRPCMultichatIdempotencyAndSecurity(t *testing.T) {
	h := setupHarness(t)
	token, _ := h.login(t, "owner")
	other, _ := h.login(t, "other")
	ctx := tokenContext(token)
	client := v1.NewChatServiceClient(h.conn)
	create := &v1.CreateConversationRequest{Client: clientContext(), Operation: operation(), Title: "one", Settings: &v1.ConversationSettings{ProfileId: "default", MemoryMode: v1.MemoryMode_MEMORY_MODE_ISOLATED}}
	first, err := client.CreateConversation(ctx, create)
	if err != nil {
		t.Fatal(err)
	}
	again, err := client.CreateConversation(ctx, create)
	if err != nil || again.ConversationId != first.ConversationId {
		t.Fatal("create lost ACK", err)
	}
	create.Title = "changed"
	if _, err = client.CreateConversation(ctx, create); status.Code(err) != codes.AlreadyExists {
		t.Fatal("changed idempotent payload", err)
	}
	create.Operation = operation()
	second, err := client.CreateConversation(ctx, create)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.GetConversation(tokenContext(other), &v1.GetConversationRequest{Client: clientContext(), ConversationId: first.ConversationId}); status.Code(err) != codes.NotFound {
		t.Fatal("foreign chat read", err)
	}
	sub := &v1.Subscription{Filter: &v1.Subscription_AllOwned{AllOwned: &emptypb.Empty{}}}
	snap, err := client.GetSnapshot(ctx, &v1.GetSnapshotRequest{Client: clientContext(), Subscription: sub})
	if err != nil {
		t.Fatal(err)
	}
	send := &v1.SendMessageRequest{Client: clientContext(), Operation: operation(), ConversationId: first.ConversationId, ClientMessageId: uuid.NewString(), ExpectedHistoryGeneration: 1, Text: "A"}
	a, err := client.SendMessage(ctx, send)
	if err != nil {
		t.Fatal(err)
	}
	send.Operation = operation()
	retry, err := client.SendMessage(ctx, send)
	if err != nil || retry.TurnId != a.TurnId {
		t.Fatal("message lost ACK", err)
	}
	send.Operation = operation()
	send.ConversationId = second.ConversationId
	send.ClientMessageId = uuid.NewString()
	send.Text = "B"
	b, err := client.SendMessage(ctx, send)
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range []*v1.Turn{a, b} {
		deadline := time.Now().Add(4 * time.Second)
		for {
			got, stepErr1 := client.GetTurn(ctx, &v1.GetTurnRequest{Client: clientContext(), ConversationId: turn.ConversationId, Lookup: &v1.GetTurnRequest_TurnId{TurnId: turn.TurnId}})
			if stepErr1 != nil {
				t.Fatal(stepErr1)
			}
			if got.State == v1.TurnState_TURN_STATE_COMPLETED {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("turn stalled", got)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	messages, err := client.ListMessages(ctx, &v1.ListMessagesRequest{Client: clientContext(), ConversationId: first.ConversationId, HistoryGeneration: 1})
	if err != nil || len(messages.Messages) != 2 || messages.Messages[1].Text != "answer: A" {
		t.Fatal("duplicate or crossed output", messages, err)
	}
	streamCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	stream, err := client.WatchEvents(streamCtx, &v1.WatchEventsRequest{Client: clientContext(), Subscription: sub, SnapshotId: snap.SnapshotId, Cursor: snap.Cursor})
	if err != nil {
		t.Fatal(err)
	}
	event, err := stream.Recv()
	if err != nil || event.DurableSeq <= snap.Watermark {
		t.Fatal("snapshot stream gap", err)
	}
	// Durable account-authority logout invalidates both idle streams and old
	// tokens, with no dependency on a refresh or a new navigation event.
	if err = h.db.RevokeUserSessions(context.Background(), h.owners["owner"]); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, err = stream.Recv()
		if err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("idle stream not revoked")
		}
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Fatal("wrong revoke status", err)
	}
	if _, err = client.GetConversation(ctx, &v1.GetConversationRequest{Client: clientContext(), ConversationId: first.ConversationId}); status.Code(err) != codes.Unauthenticated {
		t.Fatal("revoked token read", err)
	}
}

func TestAccountSecurityChangeBetweenInterceptorAndRepositoryCommit(t *testing.T) {
	h := setupHarness(t)
	token, _ := h.login(t, "owner")
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := h.server.unary(ctx, &v1.CreateConversationRequest{Client: clientContext()}, &grpc.UnaryServerInfo{FullMethod: "/iulita.mobile.v1.ChatService/CreateConversation"}, func(ctx context.Context, _ any) (any, error) {
			close(entered)
			<-release
			return h.chats.Create(ctx, h.owners["owner"], "must not commit", conversations.Settings{ProfileID: "default", MemoryMode: "isolated"})
		})
		done <- err
	}()
	<-entered
	if err := h.db.RevokeUserSessions(context.Background(), h.owners["owner"]); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err := <-done; status.Code(err) != codes.Unauthenticated {
		t.Fatal("stale authentication committed a resource", err)
	}
	list, _, err := h.chats.List(context.Background(), h.owners["owner"], true, 100, "")
	if err != nil || len(list) != 0 {
		t.Fatal("security race created conversation", list, err)
	}
}

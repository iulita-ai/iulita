package dashboard

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
	business "github.com/iulita-ai/iulita/internal/telegrambusiness"
)

func enableSecretaryFixture(t *testing.T, app *fiber.App, s *Server, scope business.Scope, at time.Time) string {
	t.Helper()
	ctx := context.Background()
	base := "/api/telegram-business/connections/" + connectionRef(scope.Key)
	for _, suffix := range []string{"", "/peers/303"} {
		st, err := s.telegramBusiness.GetState(ctx, scope.OwnerID, scope.Key)
		if err != nil {
			t.Fatal(err)
		}
		revision := st.Connection.Epoch
		if suffix != "" {
			revision = st.Peers[303].Revision
		}
		body := fmt.Sprintf(`{"expected_revision":%d,"mode":"confirmed_send","processing_consent":true,"policy_version":%q}`, revision, secretaryConsentVersion("fixture-provider"))
		if responseStatus, response := secretaryRequest(t, app, "PUT", base+suffix+"/policy", body); responseStatus != 200 {
			t.Fatalf("consent %s: %d %v", suffix, responseStatus, response)
		}
	}
	if err := s.telegramBusiness.Handle(ctx, business.Event{Key: scope.Key, OwnerID: scope.OwnerID, Generation: "fixture", UpdateID: 3, Kind: "message", PeerID: scope.PeerID, Message: &business.Message{ID: 11, Date: at, Direction: "incoming", SenderID: scope.PeerID, Text: "isolated retained fixture"}}); err != nil {
		t.Fatal(err)
	}
	return base
}

func createSecretaryFixtureDraft(t *testing.T, app *fiber.App, s *Server, scope business.Scope, base string) map[string]any {
	t.Helper()
	st, err := s.telegramBusiness.GetState(context.Background(), scope.OwnerID, scope.Key)
	if err != nil {
		t.Fatal(err)
	}
	status, response := secretaryRequest(t, app, "POST", base+"/peers/303/drafts", fmt.Sprintf(`{"expected_revision":%d,"text":"Exact owner reply"}`, st.Peers[303].Revision))
	if status != 200 {
		t.Fatalf("create draft %d %v", status, response)
	}
	return response["draft"].(map[string]any)
}

func TestSecretaryEditAfterApprovalCreatesNewApprovalAndRejects(t *testing.T) {
	app, s, _, scope := secretaryServerFixture(t)
	base := enableSecretaryFixture(t, app, s, scope, time.Now())
	draft := createSecretaryFixtureDraft(t, app, s, scope, base)
	path := "/api/telegram-business/drafts/" + draft["id"].(string)
	status, approved := secretaryRequest(t, app, "POST", path+"/approve", fmt.Sprintf(`{"expected_revision":%.0f}`, draft["revision"]))
	if status != 200 {
		t.Fatalf("first approval %d %v", status, approved)
	}
	first := approved["draft"].(map[string]any)["approval_id"]
	status, edited := secretaryRequest(t, app, "PUT", path, fmt.Sprintf(`{"expected_revision":%.0f,"text":"Changed exact reply"}`, draft["revision"]))
	if status != 200 {
		t.Fatalf("edit %d %v", status, edited)
	}
	draft = edited["draft"].(map[string]any)
	status, approved = secretaryRequest(t, app, "POST", path+"/approve", fmt.Sprintf(`{"expected_revision":%.0f}`, draft["revision"]))
	if status != 200 {
		t.Fatalf("second approval %d %v", status, approved)
	}
	second := approved["draft"].(map[string]any)
	if first == second["approval_id"] || second["text"] != "Changed exact reply" {
		t.Fatalf("old approval reused %v", second)
	}
	if responseStatus, response := secretaryRequest(t, app, "POST", path+"/reject", fmt.Sprintf(`{"expected_revision":%.0f}`, draft["revision"])); responseStatus != 200 {
		t.Fatalf("reject %d %v", responseStatus, response)
	}
	status, response := secretaryRequest(t, app, "GET", base+"/peers/303/messages", "")
	if status != 200 || len(response["drafts"].([]any)) != 0 {
		t.Fatalf("rejected draft retained %d %v", status, response)
	}
}

func TestSecretaryPeerConsentDoesNotBreakAccountPauseAndReadOnlyInspection(t *testing.T) {
	app, s, _, scope := secretaryServerFixture(t)
	base := enableSecretaryFixture(t, app, s, scope, time.Now())
	createSecretaryFixtureDraft(t, app, s, scope, base)
	st, _ := s.telegramBusiness.GetState(context.Background(), scope.OwnerID, scope.Key)
	status, response := secretaryRequest(t, app, "POST", base+"/pause", fmt.Sprintf(`{"expected_revision":%d,"paused":true}`, st.Connection.Epoch))
	if status != 200 {
		t.Fatalf("account pause after peer change %d %v", status, response)
	}
	status, response = secretaryRequest(t, app, "GET", base+"/peers/303/messages", "")
	if status != 200 || len(response["messages"].([]any)) != 1 || len(response["drafts"].([]any)) != 1 {
		t.Fatalf("owner cannot inspect paused data %d %v", status, response)
	}
	if _, err := s.telegramBusiness.Messages(context.Background(), scope); err == nil {
		t.Fatal("paused processing context remained available")
	}
}

func TestSecretaryClosedReplyWindowKeepsManualDraft(t *testing.T) {
	app, s, _, scope := secretaryServerFixture(t)
	base := enableSecretaryFixture(t, app, s, scope, time.Now().Add(-25*time.Hour))
	draft := createSecretaryFixtureDraft(t, app, s, scope, base)
	status, response := secretaryRequest(t, app, "POST", "/api/telegram-business/drafts/"+draft["id"].(string)+"/approve", fmt.Sprintf(`{"expected_revision":%.0f}`, draft["revision"]))
	if status != 409 || response["code"] != "reply_window_closed" {
		t.Fatalf("expired reply window %d %v", status, response)
	}
	status, response = secretaryRequest(t, app, "GET", base+"/peers", "")
	if status != 200 || response["peers"].([]any)[0].(map[string]any)["reply_available"] != false {
		t.Fatalf("reply deadline DTO %d %v", status, response)
	}
}

func TestSecretaryEncryptionAndProviderChangesRequireConsent(t *testing.T) {
	app, s, _, scope := secretaryServerFixture(t)
	base := enableSecretaryFixture(t, app, s, scope, time.Now())
	s.businessEncryptionEnabled = false
	status, response := secretaryRequest(t, app, "GET", "/api/telegram-business/connections", "")
	if status != 200 {
		t.Fatal(status)
	}
	conn := response["connections"].([]any)[0].(map[string]any)
	if conn["state"] != "metadata_only" || conn["processing_consent"] != false {
		t.Fatalf("plaintext activation advertised %v", conn)
	}
	st, _ := s.telegramBusiness.GetState(context.Background(), scope.OwnerID, scope.Key)
	status, _ = secretaryRequest(t, app, "PUT", base+"/policy", fmt.Sprintf(`{"expected_revision":%d,"mode":"draft_only","processing_consent":true,"policy_version":%q}`, st.Connection.Epoch, secretaryConsentVersion("fixture-provider")))
	if status != 503 {
		t.Fatalf("plaintext content consent %d", status)
	}
	s.businessEncryptionEnabled = true
	s.businessProvider = "changed-provider"
	_, response = secretaryRequest(t, app, "GET", "/api/telegram-business/connections", "")
	conn = response["connections"].([]any)[0].(map[string]any)
	if conn["processing_consent"] != false {
		t.Fatal("provider change silently retained consent")
	}
	status, _ = secretaryRequest(t, app, "PUT", base+"/policy", fmt.Sprintf(`{"expected_revision":%d,"mode":"draft_only","processing_consent":true,"policy_version":%q}`, st.Connection.Epoch, secretaryConsentVersion("fixture-provider")))
	if status != 409 {
		t.Fatalf("old provider consent accepted %d", status)
	}
}

func TestSecretaryLocalPurgeRemovesOnlyScopedUploads(t *testing.T) {
	app, s, _, scope := secretaryServerFixture(t)
	base := enableSecretaryFixture(t, app, s, scope, time.Now())
	crypto, _ := config.NewEncryptor(make([]byte, 32))
	vault, err := business.NewMediaVault(t.TempDir(), crypto)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vault.Close() })
	s.businessMedia = vault
	st, _ := s.telegramBusiness.GetState(context.Background(), scope.OwnerID, scope.Key)
	other := scope
	other.PeerID = 404
	otherRef, err := vault.Put(context.Background(), other, "other.txt", "text/plain", []byte("other peer fixture"), business.MediaAuthorityFor(st.Connection, nil))
	if err != nil {
		t.Fatal(err)
	}
	status, response := secretaryRequest(t, app, "POST", base+"/peers/303/media", `{"filename":"local.txt","mime_type":"text/plain","data":"`+base64.StdEncoding.EncodeToString([]byte("local fixture"))+`"}`)
	if status != 200 {
		t.Fatalf("upload %d %v", status, response)
	}
	ref := response["media_ref"].(string)
	status, response = secretaryRequest(t, app, "POST", base+"/peers/303/purge", fmt.Sprintf(`{"expected_revision":%d}`, st.Peers[303].Revision))
	if status != 200 {
		t.Fatalf("purge %d %v", status, response)
	}
	if _, err := vault.Read(context.Background(), scope, ref); err == nil {
		t.Fatal("purged upload still readable")
	}
	if _, err := vault.Read(context.Background(), other, otherRef); err != nil {
		t.Fatalf("other peer removed: %v", err)
	}
	if _, err := s.telegramBusiness.OwnerMessages(context.Background(), scope); err != nil && !errors.Is(err, business.ErrDenied) {
		t.Fatal(err)
	}
}

func TestSecretaryActionCancellationUsesOpaqueScopeAndExactDigest(t *testing.T) {
	app, s, _, scope := secretaryServerFixture(t)
	base := enableSecretaryFixture(t, app, s, scope, time.Now())
	st, _ := s.telegramBusiness.GetState(context.Background(), scope.OwnerID, scope.Key)
	status, response := secretaryRequest(t, app, "POST", base+"/peers/303/actions", fmt.Sprintf(`{"expected_revision":%d,"method":"sendMessage","params":{"text":"Exact isolated action"}}`, st.Peers[303].Revision))
	if status != 200 {
		t.Fatalf("prepare %d %v", status, response)
	}
	action := response["action"].(map[string]any)
	encoded, _ := json.Marshal(action)
	if strings.Contains(string(encoded), scope.Key.ConnectionID) || action["scope"] != nil || action["connection_id"] != connectionRef(scope.Key) {
		t.Fatalf("raw authority in DTO %s", encoded)
	}
	path := base + "/peers/303/actions/" + action["id"].(string) + "/cancel"
	status, _ = secretaryRequest(t, app, "POST", path, `{"digest":"changed"}`)
	if status != 409 {
		t.Fatalf("changed digest accepted %d", status)
	}
	status, response = secretaryRequest(t, app, "POST", path, fmt.Sprintf(`{"digest":%q}`, action["digest"]))
	if status != 200 || response["action"].(map[string]any)["state"] != "cancelled" {
		t.Fatalf("cancel %d %v", status, response)
	}
}

func secretaryServerFixture(t *testing.T) (*fiber.App, *Server, *sqlite.Store, business.Scope) {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.RunMigrations(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureTelegramBusiness(ctx); err != nil {
		t.Fatal(err)
	}
	user := &domain.User{ID: "secretary-owner", Username: "secretary-owner", SecurityEpoch: 1}
	if err := store.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := store.BindChannel(ctx, &domain.UserChannel{UserID: user.ID, ChannelInstanceID: "test-bot", ChannelType: "telegram", ChannelID: "101", ChannelUserID: "101", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	crypto, _ := config.NewEncryptor(make([]byte, 32))
	repo := sqlite.NewTelegramBusinessRepository(store, crypto)
	service := business.New(repo, nil, nil, business.Options{RestoreGeneration: "fixture"})
	key := business.Key{InstanceID: "test-bot", BotID: 202, ConnectionID: "opaque-telegram-connection"}
	scope := business.Scope{Key: key, OwnerID: user.ID, PeerID: 303}
	remote := business.RemoteConnection{Key: key, OwnerTelegramID: 101, OwnerChatID: 101, Enabled: true, Rights: business.Rights{CanReply: true}, ObservedAt: time.Now()}
	if err := service.Handle(ctx, business.Event{Key: key, OwnerID: user.ID, Generation: "fixture", UpdateID: 1, Kind: "connection", Connection: &remote}); err != nil {
		t.Fatal(err)
	}
	if err := service.Handle(ctx, business.Event{Key: key, OwnerID: user.ID, Generation: "fixture", UpdateID: 2, Kind: "message", PeerID: 303, Message: &business.Message{ID: 10, Date: time.Now().Add(-48 * time.Hour), Direction: "incoming", SenderID: 303, Text: "not retained before consent"}}); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store, authService: auth.NewService(store, "fixture-jwt", time.Hour, time.Hour), telegramBusiness: service, businessProvider: "fixture-provider", businessEncryptionEnabled: true}
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(auth.ContextKeyUser, &auth.Claims{UserID: user.ID, SecurityEpoch: 1})
		return c.Next()
	})
	s.registerTelegramBusiness(app.Group("/api"))
	return app, s, store, scope
}
func secretaryRequest(t *testing.T, app *fiber.App, method, path, body string) (status int, result map[string]any) {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := app.Test(request, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var value map[string]any
	data, _ := io.ReadAll(response.Body)
	if json.Unmarshal(data, &value) != nil {
		t.Fatalf("non-JSON response %d", response.StatusCode)
	}
	return response.StatusCode, value
}
func TestSecretaryOwnerPolicyDraftApprovalAndDisabledExecution(t *testing.T) {
	app, s, _, scope := secretaryServerFixture(t)
	ctx := context.Background()
	ref := connectionRef(scope.Key)
	base := "/api/telegram-business/connections/" + ref
	status, overview := secretaryRequest(t, app, "GET", "/api/telegram-business/connections", "")
	if status != 200 || overview["live_writes_enabled"] != false {
		t.Fatalf("overview=%d %v", status, overview)
	}
	st, err := s.telegramBusiness.GetState(ctx, scope.OwnerID, scope.Key)
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"expected_revision":%d,"mode":"confirmed_send","processing_consent":true,"policy_version":%q}`, st.Connection.Epoch, secretaryConsentVersion("fixture-provider"))
	if responseStatus, _ := secretaryRequest(t, app, "PUT", base+"/policy", body); responseStatus != 200 {
		t.Fatalf("connection policy=%d", responseStatus)
	}
	st, _ = s.telegramBusiness.GetState(ctx, scope.OwnerID, scope.Key)
	body = fmt.Sprintf(`{"expected_revision":%d,"mode":"confirmed_send","processing_consent":true,"policy_version":%q}`, st.Peers[303].Revision, secretaryConsentVersion("fixture-provider"))
	if responseStatus, _ := secretaryRequest(t, app, "PUT", base+"/peers/303/policy", body); responseStatus != 200 {
		t.Fatalf("peer policy=%d", responseStatus)
	}
	if err := s.telegramBusiness.Handle(ctx, business.Event{Key: scope.Key, OwnerID: scope.OwnerID, Generation: "fixture", UpdateID: 3, Kind: "message", PeerID: 303, Message: &business.Message{ID: 11, Date: time.Now(), Direction: "incoming", SenderID: 303, Text: "isolated fixture"}}); err != nil {
		t.Fatal(err)
	}
	st, _ = s.telegramBusiness.GetState(ctx, scope.OwnerID, scope.Key)
	body = fmt.Sprintf(`{"expected_revision":%d,"text":"Owner reviewed reply","generate":false}`, st.Peers[303].Revision)
	status, created := secretaryRequest(t, app, "POST", base+"/peers/303/drafts", body)
	if status != 200 {
		t.Fatalf("create=%d %v", status, created)
	}
	draft := created["draft"].(map[string]any)
	id := draft["id"].(string)
	revision := draft["revision"].(float64)
	status, approved := secretaryRequest(t, app, "POST", "/api/telegram-business/drafts/"+id+"/approve", fmt.Sprintf(`{"expected_revision":%.0f}`, revision))
	if status != 200 {
		t.Fatalf("approve=%d %v", status, approved)
	}
	draft = approved["draft"].(map[string]any)
	if draft["state"] != "approved" || draft["text"] != "Owner reviewed reply" {
		t.Fatalf("approval DTO=%v", draft)
	}
	status, result := secretaryRequest(t, app, "POST", "/api/telegram-business/drafts/"+id+"/execute", fmt.Sprintf(`{"expected_revision":%.0f,"approval_id":%q}`, revision, draft["approval_id"]))
	if status != 403 || result["code"] != "disabled" {
		t.Fatalf("external action not disabled=%d %v", status, result)
	}
	status, loaded := secretaryRequest(t, app, "GET", base+"/peers/303/messages?limit=3", "")
	if status != 200 {
		t.Fatalf("messages=%d %v", status, loaded)
	}
	if messages := loaded["messages"].([]any); len(messages) != 1 || messages[0].(map[string]any)["text"] != "isolated fixture" {
		t.Fatalf("preconsent data leaked=%v", loaded)
	}
}
func TestSecretaryRejectsForeignScopeOldEpochAndUnreviewedBody(t *testing.T) {
	app, _, store, scope := secretaryServerFixture(t)
	base := "/api/telegram-business/connections/" + connectionRef(scope.Key)
	for _, path := range []string{"/api/telegram-business/connections/foreign/peers", base + "/peers/0/messages", base + "/peers/303/messages?limit=100"} {
		status, _ := secretaryRequest(t, app, "GET", path, "")
		if status == 200 {
			t.Fatalf("accepted %s", path)
		}
	}
	if responseStatus, _ := secretaryRequest(t, app, "PUT", base+"/policy", `{"expected_revision":1,"mode":"draft_only","processing_consent":true,"policy_version":"secretary-v1","owner_id":"other"}`); responseStatus != 400 {
		t.Fatalf("unknown authority accepted %d", responseStatus)
	}
	user, err := store.GetUser(context.Background(), scope.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	user.Disabled = true
	if err := store.UpdateUser(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	status, _ := secretaryRequest(t, app, "GET", "/api/telegram-business/connections", "")
	if status != 401 {
		t.Fatalf("disabled owner=%d", status)
	}
	user.Disabled = false
	if err := store.UpdateUser(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	status, _ = secretaryRequest(t, app, "GET", "/api/telegram-business/connections", "")
	if status != 401 {
		t.Fatalf("revoked JWT resurrected=%d", status)
	}
}

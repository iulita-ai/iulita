package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/storage"
	business "github.com/iulita-ai/iulita/internal/telegrambusiness"
)

type businessRoundTrip func(*http.Request) (*http.Response, error)

func (f businessRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func businessHTTPResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func businessTestChannel(t *testing.T, transport businessRoundTrip) *Channel {
	t.Helper()
	b, err := bot.New("123:fixture", bot.WithSkipGetMe())
	if err != nil {
		t.Fatal(err)
	}
	c := &Channel{bot: b, botID: 123, instanceID: "test-instance", http: &http.Client{Transport: transport}, businessGeneration: "delivery"}
	c.SetBusinessRuntimeGuard(func(business.Scope) string { return c.businessDeliveryGeneration() })
	return c
}
func TestConnectedAccountRPCNeverRetriesOrLeaksErrors(t *testing.T) {
	for _, test := range []struct {
		name     string
		status   int
		body     string
		definite bool
	}{
		{"server failure", 500, `{"ok":false,"error_code":500,"description":"private text"}`, false},
		{"bad gateway", 502, `bad proxy`, false},
		{"Telegram rejection", 400, `{"ok":false,"error_code":400,"description":"private text"}`, true},
		{"rewritten status", 200, `{"ok":false,"error_code":400}`, false},
		{"redirect", 307, `{"ok":false,"error_code":307}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			c := businessTestChannel(t, func(*http.Request) (*http.Response, error) {
				calls++
				r := businessHTTPResponse(test.status, test.body)
				r.Header.Set("Location", "https://example.invalid/secret")
				return r, nil
			})
			err := c.businessCall(context.Background(), "sendMessage", map[string]any{"business_connection_id": "conn", "chat_id": 456, "text": "fixture"}, new(json.RawMessage))
			if err == nil || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			var e *business.APIError
			definite := errors.As(err, &e) && e.Rejected
			if definite != test.definite {
				t.Fatalf("definite=%v", definite)
			}
			if strings.Contains(err.Error(), "private text") || strings.Contains(err.Error(), "123:fixture") {
				t.Fatal("private error leaked")
			}
		})
	}
}
func TestConnectedAccountCallbacksCannotReachLegacyOwnerConfirmations(t *testing.T) {
	c := &Channel{}
	for _, from := range []int64{101, 303} {
		update := models.Update{CallbackQuery: &models.CallbackQuery{From: models.User{ID: from}, Data: "confirm:fixture", Message: models.MaybeInaccessibleMessage{Message: &models.Message{BusinessConnectionID: "conn"}}}}
		handled, err := c.handleBusinessUpdate(context.Background(), &update)
		if !handled || !errors.Is(err, business.ErrDenied) {
			t.Fatal("business callback admitted to personal handlers")
		}
	}
}
func TestConnectedAccountDecodesBotFullInfoAndNested105Quote(t *testing.T) {
	var info BotFullInfo
	if json.Unmarshal([]byte(`{"id":123,"username":"FixtureAgent","active_usernames":["FixtureAgent","Fixture_bot"],"can_connect_to_business":true,"has_topic_sidebar":true}`), &info) != nil || len(info.ActiveUsernames) != 2 || !info.CanConnectToBusiness || !info.HasTopicSidebar {
		t.Fatal("10.5 identity lost")
	}
	raw := []byte(`{"update_id":1,"business_message":{"message_id":10,"business_connection_id":"conn","date":100,"chat":{"id":303,"type":"private"},"from":{"id":303,"is_bot":false,"first_name":"Fixture"},"rich_message":{"blocks":[{"type":"expandable_blockquote","blocks":[{"type":"paragraph","text":"Nested source"}]}]}}}`)
	update, err := decodeBusinessCompatibleUpdate(raw)
	if err != nil {
		t.Fatal(err)
	}
	text, _ := normalizedTelegramContent(update.BusinessMessage)
	if !strings.Contains(text, "Nested source") {
		t.Fatalf("quote display content lost: %q", text)
	}
}

type businessTransportMemory struct {
	mu             sync.Mutex
	states         map[business.Key]business.State
	failNextUpdate bool
}

func (r *businessTransportMemory) Get(_ context.Context, key business.Key) (*business.State, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.states[key]
	if !ok {
		return nil, nil
	}
	data, _ := json.Marshal(state)
	var cloned business.State
	_ = json.Unmarshal(data, &cloned)
	return &cloned, nil
}
func (r *businessTransportMemory) List(_ context.Context, owner string) ([]business.State, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []business.State
	for sIndex := range r.states {
		s := r.states[sIndex]
		if s.Connection.OwnerID == owner {
			data, _ := json.Marshal(s)
			var cloned business.State
			_ = json.Unmarshal(data, &cloned)
			out = append(out, cloned)
		}
	}
	return out, nil
}
func (r *businessTransportMemory) Update(_ context.Context, key business.Key, fn func(*business.State) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failNextUpdate {
		r.failNextUpdate = false
		return errors.New("fixture admission database failure")
	}
	data, _ := json.Marshal(r.states[key])
	var state business.State
	_ = json.Unmarshal(data, &state)
	state.Connection.Key = key
	if checkErr := fn(&state); checkErr != nil {
		return checkErr
	}
	r.states[key] = state
	return nil
}

func TestConnectedAccountMultipartUsesOwnedEncryptedBytesAndRejectsOldEpoch(t *testing.T) {
	ctx := context.Background()
	key := business.Key{InstanceID: "test-instance", BotID: 123, ConnectionID: "conn"}
	scope := business.Scope{Key: key, OwnerID: "owner"}
	connection := business.Connection{RemoteConnection: business.RemoteConnection{Key: key, OwnerTelegramID: 101, OwnerChatID: 101, Enabled: true}, OwnerID: "owner", OwnerSecurityEpoch: 1, Epoch: 1, VerifiedAt: time.Now(), RuntimeGeneration: "run", RestoreGeneration: "restore", Policy: business.PeerPolicy{Enabled: true, Consent: true, Mode: business.ModeConfirm}}
	repo := &businessTransportMemory{states: map[business.Key]business.State{key: {Connection: connection}}}
	crypto, _ := config.NewEncryptor(make([]byte, 32))
	vault, err := business.NewMediaVault(t.TempDir(), crypto)
	if err != nil {
		t.Fatal(err)
	}
	var imageBytes bytes.Buffer
	_ = png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	ref, err := vault.Put(ctx, scope, "fixture.png", "image/png", imageBytes.Bytes(), business.MediaAuthorityFor(connection, nil))
	if err != nil {
		t.Fatal(err)
	}
	service := business.New(repo, nil, nil, business.Options{Media: vault, RuntimeGeneration: "run", RestoreGeneration: "restore"})
	calls := 0
	c := businessTestChannel(t, func(request *http.Request) (*http.Response, error) {
		calls++
		_, params, parseErr := mime.ParseMediaType(request.Header.Get("Content-Type"))
		if parseErr != nil || params["boundary"] == "" {
			t.Fatal("not multipart")
		}
		reader := multipart.NewReader(request.Body, params["boundary"])
		fields := map[string][]byte{}
		for {
			part, readErr := reader.NextPart()
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				t.Fatal(readErr)
			}
			fields[part.FormName()], _ = io.ReadAll(part)
		}
		if !bytes.Equal(fields["bfile0"], imageBytes.Bytes()) || !bytes.Contains(fields["photo"], []byte("attach://bfile0")) || string(fields["business_connection_id"]) != "conn" {
			t.Fatal("wrong multipart bytes/authority")
		}
		return businessHTTPResponse(200, `{"ok":true,"result":true}`), nil
	})
	c.business = service
	photo, _ := json.Marshal(map[string]string{"type": "static", "photo": ref})
	request := business.Request{Action: business.Action{Scope: scope, Method: business.SetProfilePhoto, Params: business.Params{"photo": photo}}}
	payload, err := request.Payload()
	if err != nil {
		t.Fatal(err)
	}
	if checkErr := c.businessMultipartCall(ctx, request, payload, new(json.RawMessage)); checkErr != nil {
		t.Fatal(checkErr)
	}
	if calls != 1 {
		t.Fatal("unexpected calls")
	}
	_ = repo.Update(ctx, key, func(st *business.State) error { st.Connection.Epoch++; return nil })
	if err := c.businessMultipartCall(ctx, request, payload, new(json.RawMessage)); !errors.Is(err, business.ErrStale) || calls != 1 {
		t.Fatalf("old media authority admitted: %v", err)
	}
}

type businessFixtureOwner struct{ storage.Repository }

func (businessFixtureOwner) ResolveTelegramBusinessOwner(context.Context, string, int64) (string, error) {
	return "owner", nil
}

type businessPollFixture struct {
	business.PollRepository
	attempts int
	cursor   int64
	cancel   context.CancelFunc
}

func (*businessPollFixture) AcquireTelegramBusinessPoll(context.Context, string, int64, string, time.Time, time.Duration) (bool, error) {
	return true, nil
}
func (*businessPollFixture) ReleaseTelegramBusinessPoll(context.Context, string, int64, string) error {
	return nil
}
func (r *businessPollFixture) LoadTelegramBusinessPoll(context.Context, string, int64) (business.PollState, error) {
	return business.PollState{NextOffset: r.cursor}, nil
}
func (*businessPollFixture) PendingTelegramBusinessPoll(context.Context, string, int64, int) ([]business.PollEntry, error) {
	return nil, nil
}
func (r *businessPollFixture) StageTelegramBusinessPoll(_ context.Context, _ string, _ int64, _ string, next int64, entries []business.PollEntry, _ time.Time) error {
	r.attempts++
	if len(entries) != 0 {
		return errors.New("business payload in ordinary inbox")
	}
	if r.attempts == 1 {
		return errors.New("fixture DB unavailable")
	}
	r.cursor = next
	r.cancel()
	return nil
}
func TestConnectedAccountPollingNeverAcknowledgesFailedAdmission(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	poll := &businessPollFixture{cancel: cancel}
	repo := &businessTransportMemory{states: map[business.Key]business.State{}}
	service := business.New(repo, nil, nil, business.Options{})
	var offsets []int64
	c := businessTestChannel(t, func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, "getWebhookInfo") {
			return businessHTTPResponse(200, `{"ok":true,"result":{"url":""}}`), nil
		}
		var params struct {
			Offset int64 `json:"offset"`
		}
		_ = json.NewDecoder(request.Body).Decode(&params)
		offsets = append(offsets, params.Offset)
		return businessHTTPResponse(200, `{"ok":true,"result":[{"update_id":1,"business_connection":{"id":"conn","user":{"id":101,"is_bot":false,"first_name":"Fixture"},"user_chat_id":101,"date":100,"rights":{"can_reply":true},"is_enabled":true}}]}`), nil
	})
	c.business = service
	c.businessPoll = poll
	c.store = businessFixtureOwner{}
	_ = c.pollBusinessUpdates(ctx, func(models.Update) { t.Fatal("business event entered ordinary dispatcher") })
	if len(offsets) != 2 || offsets[0] != 0 || offsets[1] != 0 || poll.cursor != 2 {
		t.Fatalf("ACK advanced beyond failed commit: offsets=%v cursor=%d", offsets, poll.cursor)
	}
}

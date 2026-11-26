package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	business "github.com/iulita-ai/iulita/internal/telegrambusiness"
)

type deliveryFixtureAPI struct{ channel *Channel }

func (a deliveryFixtureAPI) GetConnection(ctx context.Context, key business.Key) (business.RemoteConnection, error) {
	return a.channel.BusinessConnection(ctx, key)
}
func (a deliveryFixtureAPI) Execute(ctx context.Context, r business.Request) (business.Receipt, error) {
	return a.channel.ExecuteBusiness(ctx, r)
}

type deliveryGenerator func(context.Context, business.Scope, []business.Message) (string, error)

func (g deliveryGenerator) Generate(ctx context.Context, s business.Scope, m []business.Message) (string, error) {
	return g(ctx, s, m)
}

type deliveryBudget struct{}

func (deliveryBudget) Reserve(context.Context, business.BudgetRequest) (business.Reservation, error) {
	return business.Reservation{ID: "fixture"}, nil
}
func (deliveryBudget) Release(context.Context, business.Reservation) error { return nil }

type deliveryOwner struct{ businessFixtureOwner }

func (deliveryOwner) TelegramBusinessIngressOwner(context.Context, business.Key) (string, error) {
	return "owner", nil
}

type deliveryPoll struct {
	mu      sync.Mutex
	entries map[int64]business.PollEntry
	after   func()
	business.PollRepository
	staged atomic.Bool
	calls  int
	fail   bool
	cursor int64
}

func (p *deliveryPoll) StageTelegramBusinessPoll(_ context.Context, _ string, _ int64, _ string, next int64, entries []business.PollEntry, _ time.Time) error {
	p.calls++
	if p.fail {
		return errors.New("fixture database failure")
	}
	p.cursor = next
	p.mu.Lock()
	if p.entries == nil {
		p.entries = make(map[int64]business.PollEntry)
	}
	for entryIndex := range entries {
		entry := entries[entryIndex]
		p.entries[entry.UpdateID] = entry
	}
	p.mu.Unlock()
	p.staged.Store(true)
	if p.after != nil {
		p.after()
	}
	return nil
}

func (p *deliveryPoll) AckTelegramBusinessPoll(_ context.Context, _ string, _, id int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.entries, id)
	return nil
}
func (p *deliveryPoll) pending() []business.PollEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]business.PollEntry, 0, len(p.entries))
	for eIndex := range p.entries {
		e := p.entries[eIndex]
		out = append(out, e)
	}
	return out
}

func deliveryFixture(t *testing.T) (*Channel, *businessTransportMemory, *deliveryPoll, business.Scope, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	poll := &deliveryPoll{}
	c := businessTestChannel(t, func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "getBusinessConnection") {
			t.Error("unexpected Telegram action", r.URL.Path)
			return nil, errors.New("unexpected")
		}
		return businessHTTPResponse(200, `{"ok":true,"result":{"id":"conn","user":{"id":101},"user_chat_id":101,"is_enabled":true,"rights":{"can_reply":true,"can_delete_sent_messages":true}}}`), nil
	})
	repo := &businessTransportMemory{states: map[business.Key]business.State{}}
	service := business.New(repo, deliveryFixtureAPI{c}, deliveryGenerator(func(context.Context, business.Scope, []business.Message) (string, error) {
		if !poll.staged.Load() {
			t.Error("provider ran before durable batch stage")
		}
		calls.Add(1)
		return "Scoped draft", nil
	}), business.Options{LiveWrites: true, Budget: deliveryBudget{}, RuntimeGeneration: "runtime"})
	c.SetBusinessService(service)
	c.businessPoll = poll
	c.store = deliveryOwner{}
	t.Cleanup(func() { c.wg.Wait() })
	scope := business.Scope{Key: business.Key{InstanceID: c.instanceID, BotID: c.botID, ConnectionID: "conn"}, OwnerID: "owner", PeerID: 303}
	remote := business.RemoteConnection{Key: scope.Key, OwnerTelegramID: 101, OwnerChatID: 101, Enabled: true, Rights: business.Rights{CanReply: true, CanDeleteSentMessages: true}}
	if checkErr := service.Handle(context.Background(), business.Event{Key: scope.Key, OwnerID: "owner", Generation: c.businessDeliveryGeneration(), UpdateID: 1, Kind: "connection", Connection: &remote}); checkErr != nil {
		t.Fatal(checkErr)
	}
	state, _ := repo.Get(context.Background(), scope.Key)
	policy := business.PeerPolicy{Enabled: true, Consent: true, Mode: business.ModeDraft, Provider: "fixture", TokenOnly: true}
	if checkErr := service.Configure(context.Background(), business.ConfigureInput{Scope: business.Scope{Key: scope.Key, OwnerID: "owner"}, ExpectedRevision: state.Connection.Epoch, Policy: policy}); checkErr != nil {
		t.Fatal(checkErr)
	}
	if checkErr := service.Configure(context.Background(), business.ConfigureInput{Scope: scope, Policy: policy}); checkErr != nil {
		t.Fatal(checkErr)
	}
	return c, repo, poll, scope, calls
}
func deliveryMessage(updateID, id int) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"update_id":%d,"business_message":{"message_id":%d,"business_connection_id":"conn","date":%d,"chat":{"id":303,"type":"private"},"from":{"id":303,"is_bot":false},"text":"Hello"}}`, updateID, id, time.Now().Unix()))
}

func TestBusinessGenerationWaitsForWholeBatchStageAndRetriesAdmission(t *testing.T) {
	c, _, poll, _, calls := deliveryFixture(t)
	ctx := context.Background()
	batch := []json.RawMessage{deliveryMessage(10, 2)}
	poll.fail = true
	if e := c.stageBusinessBatch(ctx, "lease", batch); e == nil {
		t.Fatal("failed stage accepted")
	}
	c.wg.Wait()
	if calls.Load() != 0 || poll.cursor != 0 {
		t.Fatal("failed commit released work or ACK")
	}
	poll.fail = false
	if checkErr := c.stageBusinessBatch(ctx, "lease", batch); checkErr != nil {
		t.Fatal(checkErr)
	}
	c.wg.Wait()
	if calls.Load() != 1 || poll.cursor != 11 {
		t.Fatal("deduplicated retry lost current candidate", calls.Load(), poll.cursor)
	}
}
func TestBusinessBatchControlAndDeletionPrecedeContent(t *testing.T) {
	for _, kind := range []string{"revoke", "delete", "pause"} {
		t.Run(kind, func(t *testing.T) {
			c, repo, poll, scope, calls := deliveryFixture(t)
			batch := []json.RawMessage{deliveryMessage(10, 2)}
			switch kind {
			case "revoke":
				batch = append(batch, json.RawMessage(`{"update_id":11,"business_connection":{"id":"conn","user":{"id":101},"user_chat_id":101,"date":1,"rights":{},"is_enabled":false}}`))
			case "delete":
				batch = append(batch, json.RawMessage(`{"update_id":11,"deleted_business_messages":{"business_connection_id":"conn","chat":{"id":303,"type":"private"},"message_ids":[2]}}`))
			case "pause":
				state, _ := repo.Get(context.Background(), scope.Key)
				if checkErr := c.business.Pause(context.Background(), business.Scope{Key: scope.Key, OwnerID: "owner"}, state.Connection.Epoch, true); checkErr != nil {
					t.Fatal(checkErr)
				}
			}
			if checkErr := c.stageBusinessBatch(context.Background(), "lease", batch); checkErr != nil {
				t.Fatal("known policy/tombstone poisoned cursor", checkErr)
			}
			c.wg.Wait()
			if calls.Load() != 0 || poll.cursor == 0 {
				t.Fatal("fenced batch generated or failed ACK", calls.Load(), poll.cursor)
			}
		})
	}
}
func TestBusinessCandidateCapacityIsBounded(t *testing.T) {
	b := &businessCandidates{}
	for i := 1; i <= 101; i++ {
		b.add(businessCandidate{scope: business.Scope{PeerID: int64(i)}})
	}
	if len(b.items) != 100 {
		t.Fatal(len(b.items))
	}
}
func TestBusinessReceiptRequiresExactRemoteScopeAndResult(t *testing.T) {
	scope := business.Scope{Key: business.Key{InstanceID: "i", BotID: 1, ConnectionID: "conn"}, OwnerID: "owner", PeerID: 303}
	for _, test := range []struct {
		name   string
		method business.Method
		raw    string
		ok     bool
	}{
		{"correct message", business.SendText, `{"message_id":5,"business_connection_id":"conn","chat":{"id":303}}`, true},
		{"foreign peer", business.SendText, `{"message_id":5,"business_connection_id":"conn","chat":{"id":999}}`, false},
		{"foreign connection", business.SendText, `{"message_id":5,"business_connection_id":"other","chat":{"id":303}}`, false},
		{"missing connection", business.SendText, `{"message_id":5,"chat":{"id":303}}`, false},
		{"missing id", business.SendText, `{"business_connection_id":"conn","chat":{"id":303}}`, false},
		{"story", business.PostStory, `{"id":7,"chat":{"id":101}}`, true},
		{"foreign story owner", business.PostStory, `{"id":7,"chat":{"id":303}}`, false},
		{"boolean false", business.SetBio, `false`, false},
		{"boolean true", business.SetBio, `true`, true},
		{"empty stars", business.GetStarBalance, `{}`, false},
		{"zero stars", business.GetStarBalance, `{"amount":0}`, true},
		{"invalid nanostars", business.GetStarBalance, `{"amount":1,"nanostar_amount":1000000000}`, false},
		{"empty gifts", business.GetGifts, `{"total_count":0,"gifts":[]}`, true},
		{"missing gifts", business.GetGifts, `{"total_count":0}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, e := parseBusinessReceipt(business.Request{Action: business.Action{Scope: scope, Method: test.method}}, 101, json.RawMessage(test.raw))
			if (e == nil) != test.ok {
				t.Fatal(e)
			}
			if e != nil && !errors.Is(e, business.ErrUnknownOutcome) {
				t.Fatal("mismatched successful response treated as definite failure")
			}
		})
	}
}

func TestIsolatedBotAPIEnvironmentDoesNotGrantSecretaryScope(t *testing.T) {
	c, _, _, scope, _ := deliveryFixture(t)
	c.testEnvironment = true
	if _, e := c.BusinessConnection(context.Background(), scope.Key); !errors.Is(e, business.ErrUnsupported) {
		t.Fatal(e)
	}
	if _, e := c.ExecuteBusiness(context.Background(), business.Request{}); !errors.Is(e, business.ErrUnsupported) {
		t.Fatal(e)
	}
	handled, e := c.handleBusinessUpdate(context.Background(), &models.Update{BusinessMessage: &models.Message{BusinessConnectionID: "conn"}})
	if !handled || !errors.Is(e, business.ErrUnsupported) {
		t.Fatal(e)
	}
	handled, e = c.handleBusinessUpdate(context.Background(), &models.Update{Message: &models.Message{Text: "ordinary bot"}})
	if handled || e != nil {
		t.Fatal("isolated normal bot disabled", e)
	}
}

func TestBusinessQuoteIsScopedMaskedAndUntrusted(t *testing.T) {
	c, _, _, scope, _ := deliveryFixture(t)
	credential := "pass" + "word = " + strings.Repeat("q", 12)
	quote := "Quoted " + credential + " " + strings.Repeat("я", 3000)
	m := &models.Message{ID: 2, BusinessConnectionID: "conn", Date: int(time.Now().Unix()), Chat: models.Chat{ID: 303, Type: models.ChatTypePrivate}, From: &models.User{ID: 303}, Text: "Current body", Quote: &models.TextQuote{Text: quote}, ReplyToMessage: &models.Message{ID: 99, Chat: models.Chat{ID: 999, Type: models.ChatTypePrivate}, Text: "foreign private source must not be read"}}
	handled, e := c.handleBusinessUpdate(context.Background(), &models.Update{ID: 10, BusinessMessage: m})
	if !handled || e != nil {
		t.Fatal(e)
	}
	messages, e := c.business.Messages(context.Background(), scope)
	if e != nil || len(messages) != 1 {
		t.Fatal(e, len(messages))
	}
	text := messages[0].Text
	if !strings.Contains(text, "[Untrusted quoted passage") || !strings.HasSuffix(text, "Current body") || strings.Contains(text, credential) || strings.Contains(text, "foreign private source") || len(text) > 4400 {
		t.Fatal("quote scope/masking/boundary lost")
	}
}

func TestExportedBusinessExecuteRejectsUnclaimedIntentBeforeHTTP(t *testing.T) {
	c, _, _, scope, _ := deliveryFixture(t)
	before := 0
	base := c.http.Transport
	c.http.Transport = businessRoundTrip(func(r *http.Request) (*http.Response, error) { before++; return base.RoundTrip(r) })
	_, e := c.ExecuteBusiness(context.Background(), business.Request{Action: business.Action{ID: "not-claimed", Scope: scope, Method: business.SendText, Params: business.Params{"text": json.RawMessage(`"not approved"`)}}})
	var rejected *business.APIError
	if !errors.As(e, &rejected) || !rejected.Rejected || before != 0 {
		t.Fatal("unclaimed exported execution reached HTTP", before, e)
	}
}

func TestRemoteMismatchedReceiptRemainsUnknownAndCannotReplay(t *testing.T) {
	c, repo, _, scope, _ := deliveryFixture(t)
	if e := repo.Update(context.Background(), scope.Key, func(st *business.State) error {
		st.Connection.Policy.Mode = business.ModeConfirm
		p := st.Peers[303]
		p.Policy.Mode = business.ModeConfirm
		p.Policy.AllowedMethods = []business.Method{business.SendText}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	// Capture a real retained incoming source before creating the approved draft.
	update, _ := decodeBusinessCompatibleUpdate(deliveryMessage(10, 2))
	if _, e := c.handleBusinessUpdate(context.Background(), &update); e != nil {
		t.Fatal(e)
	}
	d, e := c.business.CreateDraft(context.Background(), scope, "Exact approved reply")
	if e != nil {
		t.Fatal(e)
	}
	a, e := c.business.Prepare(context.Background(), business.PrepareInput{Scope: scope, Method: business.SendText, Params: business.Params{"text": json.RawMessage(`"Exact approved reply"`)}, DraftID: d.ID})
	if e != nil {
		t.Fatal(e)
	}
	a, e = c.business.Approve(context.Background(), business.ApprovalInput{Scope: scope, ActionID: a.ID, Digest: a.Digest, Step: 1})
	if e != nil {
		t.Fatal(e)
	}
	sends := 0
	base := c.http.Transport
	c.http.Transport = businessRoundTrip(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "sendMessage") {
			sends++
			return businessHTTPResponse(200, `{"ok":true,"result":{"message_id":55,"business_connection_id":"conn","chat":{"id":999}}}`), nil
		}
		return base.RoundTrip(r)
	})
	if _, e = c.business.Execute(context.Background(), scope, a.ID); !errors.Is(e, business.ErrUnknownOutcome) {
		t.Fatal(e)
	}
	stored, e := c.business.Action(context.Background(), scope, a.ID)
	if e != nil || stored.State != "unknown" {
		t.Fatal("mismatched response marked sent", e)
	}
	if _, e = c.business.Execute(context.Background(), scope, a.ID); e == nil || sends != 1 {
		t.Fatal("unknown operation replayed", sends, e)
	}
}

func TestBusinessFailedLaterStageCannotEscapeThroughActiveCoalescer(t *testing.T) {
	c, repo, poll, scope, calls := deliveryFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	// Replace only the generator through a new service before any intake/work.
	service := business.New(repo, deliveryFixtureAPI{c}, deliveryGenerator(func(context.Context, business.Scope, []business.Message) (string, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return "Current draft", nil
	}), business.Options{Budget: deliveryBudget{}, RuntimeGeneration: "runtime"})
	c.business = service
	if checkErr := c.stageBusinessBatch(context.Background(), "lease", []json.RawMessage{deliveryMessage(10, 2)}); checkErr != nil {
		t.Fatal(checkErr)
	}
	<-entered
	poll.fail = true
	if e := c.stageBusinessBatch(context.Background(), "lease", []json.RawMessage{deliveryMessage(11, 3)}); e == nil {
		t.Fatal("stage failure ignored")
	}
	close(release)
	c.wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("old active coalescer generated unadmitted revision", calls.Load())
	}
	poll.fail = false
	if checkErr := c.stageBusinessBatch(context.Background(), "lease", []json.RawMessage{deliveryMessage(11, 3)}); checkErr != nil {
		t.Fatal(checkErr)
	}
	c.wg.Wait()
	if calls.Load() != 2 {
		t.Fatal("successful retry did not release latest revision", calls.Load())
	}
	state, _ := repo.Get(context.Background(), scope.Key)
	if state.Peers[303].ProcessRevision != state.Peers[303].Revision {
		t.Fatal("successful admission not persisted")
	}
}
func TestForeignBusinessBotCannotCreateOwnEditOrDeleteProof(t *testing.T) {
	for _, sender := range []int64{101, 303} {
		t.Run(fmt.Sprint(sender), func(t *testing.T) {
			c, repo, _, scope, _ := deliveryFixture(t)
			if e := repo.Update(context.Background(), scope.Key, func(st *business.State) error {
				st.Connection.Policy.Mode = business.ModeConfirm
				p := st.Peers[303]
				p.Policy.Mode = business.ModeConfirm
				p.Policy.AllowedMethods = []business.Method{business.EditText, business.DeleteSent}
				return nil
			}); e != nil {
				t.Fatal(e)
			}
			update := models.Update{ID: 10, BusinessMessage: &models.Message{ID: 2, BusinessConnectionID: "conn", Date: int(time.Now().Unix()), Chat: models.Chat{ID: 303, Type: models.ChatTypePrivate}, From: &models.User{ID: sender}, SenderBusinessBot: &models.User{ID: 999}, Text: "Other bot reply"}}
			if _, e := c.handleBusinessUpdate(context.Background(), &update); e != nil {
				t.Fatal(e)
			}
			messages, e := c.business.Messages(context.Background(), scope)
			if e != nil || len(messages) != 1 {
				t.Fatal(e)
			}
			if messages[0].Direction == "bot_outgoing" || !messages[0].SenderIsBot {
				t.Fatal("foreign business bot gained owned-message proof")
			}
			for _, method := range []business.Method{business.EditText, business.DeleteSent} {
				params := business.Params{"message_id": json.RawMessage(`2`), "text": json.RawMessage(`"alter"`)}
				if method == business.DeleteSent {
					params = business.Params{"message_ids": json.RawMessage(`[2]`)}
				}
				if _, e := c.business.Prepare(context.Background(), business.PrepareInput{Scope: scope, Method: method, Params: params}); e == nil {
					t.Fatal("foreign bot message accepted as owned", method)
				}
			}
		})
	}
}

func TestBusinessAdmissionDBFailureAfterStageRetainsDurableCandidate(t *testing.T) {
	c, repo, poll, _, calls := deliveryFixture(t)
	poll.after = func() { repo.mu.Lock(); repo.failNextUpdate = true; repo.mu.Unlock() }
	if checkErr := c.stageBusinessBatch(context.Background(), "lease", []json.RawMessage{deliveryMessage(10, 2)}); checkErr != nil {
		t.Fatal(checkErr)
	}
	c.wg.Wait()
	pending := poll.pending()
	if poll.cursor != 11 || len(pending) != 1 || calls.Load() != 0 {
		t.Fatal("post-stage failure lost candidate or started model", poll.cursor, len(pending), calls.Load())
	}
	if handled, e := c.drainBusinessCandidate(context.Background(), pending[0]); !handled || e != nil {
		t.Fatal(e)
	}
	c.wg.Wait()
	if calls.Load() != 1 || len(poll.pending()) != 0 {
		t.Fatal("durable candidate recovery failed", calls.Load())
	}
}
func TestWireCannotForgeLocalBusinessCandidateAndOldGenerationCannotReplay(t *testing.T) {
	c, repo, poll, scope, calls := deliveryFixture(t)
	state, _ := repo.Get(context.Background(), scope.Key)
	candidate := businessCandidate{scope: scope, messageID: 2, revision: 0, expectedRevision: state.Peers[303].Revision, updateID: 10, generation: c.businessDeliveryGeneration()}
	entry, e := encodeBusinessCandidate(candidate)
	if e != nil {
		t.Fatal(e)
	}
	if e = c.stageBusinessBatch(context.Background(), "lease", []json.RawMessage{entry.Payload}); e != nil {
		t.Fatal(e)
	}
	c.wg.Wait()
	if calls.Load() != 0 || len(poll.pending()) != 0 || poll.cursor != 11 {
		t.Fatal("wire forged internal receipt")
	}
	poll.mu.Lock()
	poll.entries[entry.UpdateID] = entry
	poll.mu.Unlock()
	c.rotateBusinessGeneration("new-runtime-generation")
	handled, e := c.drainBusinessCandidate(context.Background(), entry)
	if !handled || e != nil || calls.Load() != 0 || len(poll.pending()) != 0 {
		t.Fatal("old generation receipt replayed", e)
	}
}

func TestBusinessOwnerAllowlistBlocksNewGrantButKeepsRevocationAndDeleteCleanup(t *testing.T) {
	c, repo, _, scope, _ := deliveryFixture(t)
	update, _ := decodeBusinessCompatibleUpdate(deliveryMessage(10, 2))
	if _, e := c.handleBusinessUpdate(context.Background(), &update); e != nil {
		t.Fatal(e)
	}
	c.allowedIDs = map[int64]struct{}{999: {}}
	if _, e := c.BusinessConnection(context.Background(), scope.Key); !errors.Is(e, business.ErrDenied) {
		t.Fatal("disallowed metadata owner accepted", e)
	}
	grant := models.Update{ID: 11, BusinessConnection: &models.BusinessConnection{ID: "conn", User: models.User{ID: 101}, UserChatID: 101, IsEnabled: true}}
	if _, e := c.handleBusinessUpdate(context.Background(), &grant); !errors.Is(e, business.ErrDenied) {
		t.Fatal("disallowed owner grant accepted", e)
	}
	revoke := grant
	revoke.ID = 12
	revoked := *grant.BusinessConnection
	revoked.IsEnabled = false
	revoke.BusinessConnection = &revoked
	if _, e := c.handleBusinessUpdate(context.Background(), &revoke); e != nil {
		t.Fatal("allowlist blocked known revocation", e)
	}
	state, _ := repo.Get(context.Background(), scope.Key)
	if state.Connection.Enabled {
		t.Fatal("revocation did not close connection")
	}
	deleted := models.Update{ID: 13, DeletedBusinessMessages: &models.BusinessMessagesDeleted{BusinessConnectionID: "conn", Chat: models.Chat{ID: 303, Type: models.ChatTypePrivate}, MessageIDs: []int{2}}}
	if _, e := c.handleBusinessUpdate(context.Background(), &deleted); e != nil {
		t.Fatal("allowlist blocked cleanup", e)
	}
	state, _ = repo.Get(context.Background(), scope.Key)
	for _, m := range state.Peers[303].Messages {
		if m.ID == 2 && (!m.Deleted || m.Text != "") {
			t.Fatal("deleted source retained")
		}
	}
}
func TestBusinessMetadataRequestHasBoundedChildDeadline(t *testing.T) {
	c := businessTestChannel(t, func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		remaining := time.Until(deadline)
		if !ok || remaining > 5*time.Second || remaining < 4*time.Second {
			t.Error("unbounded metadata deadline", remaining)
		}
		return businessHTTPResponse(200, `{"ok":true,"result":{"id":"conn","user":{"id":101},"user_chat_id":101,"is_enabled":true}}`), nil
	})
	_, e := c.BusinessConnection(context.Background(), business.Key{InstanceID: c.instanceID, BotID: c.botID, ConnectionID: "conn"})
	if e != nil {
		t.Fatal(e)
	}
}

func TestBusinessPhotoSelectsBestBoundedVariantInsteadOfOversizedLast(t *testing.T) {
	photos := []models.PhotoSize{{FileID: "thumbnail", Width: 160, Height: 160, FileSize: 10000}, {FileID: "adequate", Width: 1024, Height: 768, FileSize: 200000}, {FileID: "oversized", Width: 3000, Height: 2000, FileSize: business.MaxMediaBytes + 1}}
	chosen := businessPhotoReference(photos)
	if chosen.Kind != "photo" || chosen.FileID != "adequate" || chosen.Bytes != 200000 {
		t.Fatal("usable rendition rejected", chosen)
	}
	reversed := []models.PhotoSize{photos[2], photos[1], photos[0]}
	if businessPhotoReference(reversed) != chosen {
		t.Fatal("selection depends on order")
	}
	if missing := businessPhotoReference([]models.PhotoSize{photos[2], {FileID: "unknown-size", Width: 800, Height: 600}}); missing.Kind != "unsupported_photo" || missing.FileID != "" {
		t.Fatal("unsupported variant fabricated usable bytes")
	}
}

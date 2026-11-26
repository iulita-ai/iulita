package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/domain"
	business "github.com/iulita-ai/iulita/internal/telegrambusiness"
	"github.com/uptrace/bun"
)

func businessStoreFixture(t *testing.T) (*Store, *TelegramBusinessRepository, business.Key, string) {
	t.Helper()
	s := newTestStore(t)
	return businessRepositoryFixture(t, s)
}

func businessRepositoryFixture(t *testing.T, s *Store) (*Store, *TelegramBusinessRepository, business.Key, string) {
	t.Helper()
	ctx := context.Background()
	if checkErr1 := s.EnsureTelegramBusiness(ctx); checkErr1 != nil {
		t.Fatal(checkErr1)
	}
	owner := uuid.NewString()
	if checkErr2 := s.CreateUser(ctx, &domain.User{ID: owner, Username: "business-owner"}); checkErr2 != nil {
		t.Fatal(checkErr2)
	}
	if checkErr3 := s.BindChannel(ctx, &domain.UserChannel{UserID: owner, ChannelInstanceID: "instance", ChannelType: "telegram", ChannelID: "101", ChannelUserID: "101", Enabled: true}); checkErr3 != nil {
		t.Fatal(checkErr3)
	}
	crypto, err := config.NewEncryptor(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	r := NewTelegramBusinessRepository(s, crypto)
	key := business.Key{InstanceID: "instance", BotID: 202, ConnectionID: "connection"}
	if checkErr4 := r.Update(ctx, key, func(state *business.State) error {
		state.Connection.OwnerID = owner
		state.Connection.OwnerTelegramID = 101
		state.Connection.OwnerChatID = 101
		state.Connection.Enabled = true
		state.Connection.Epoch = 1
		state.Connection.RestoreGeneration = "runtime"
		state.Connection.Policy = business.PeerPolicy{Enabled: true, Consent: true, Mode: business.ModeDraft, Provider: "provider"}
		state.Peers = map[int64]*business.Peer{303: {ID: 303, Policy: business.PeerPolicy{Enabled: true, Consent: true, Mode: business.ModeDraft, Provider: "provider", Revision: 1}, Revision: 1}}
		return nil
	}); checkErr4 != nil {
		t.Fatal(checkErr4)
	}
	return s, r, key, owner
}

type businessBeginObserver struct {
	begin chan struct{}
	once  sync.Once
}

func (h *businessBeginObserver) BeforeQuery(ctx context.Context, event *bun.QueryEvent) context.Context {
	if event.Query == "BEGIN IMMEDIATE" {
		h.once.Do(func() { close(h.begin) })
	}
	return ctx
}

func (*businessBeginObserver) AfterQuery(context.Context, *bun.QueryEvent) {}

func TestTelegramBusinessIndependentWriterRechecksCommittedEpoch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	path := t.TempDir() + "/business.sqlite"
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if checkErr5 := s.Close(); checkErr5 != nil {
			t.Error(checkErr5)
		}
	})
	if checkErr6 := s.RunMigrations(ctx); checkErr6 != nil {
		t.Fatal(checkErr6)
	}
	_, r, key, _ := businessRepositoryFixture(t, s)
	peerStore, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if checkErr7 := peerStore.Close(); checkErr7 != nil {
			t.Error(checkErr7)
		}
	})
	other := NewTelegramBusinessRepository(peerStore, r.crypto)
	observer := &businessBeginObserver{begin: make(chan struct{})}
	peerStore.db.AddQueryHook(observer)
	entered, release := make(chan struct{}), make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- r.Update(ctx, key, func(state *business.State) error {
			state.Connection.Epoch++
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- other.Update(ctx, key, func(state *business.State) error {
			if state.Connection.Epoch != 1 {
				return business.ErrStale
			}
			state.Peers[303].Messages = []business.Message{{ID: 1, Text: "late-old-epoch"}}
			return nil
		})
	}()
	select {
	case <-observer.begin:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	close(release)
	if checkErr8 := <-firstDone; checkErr8 != nil {
		t.Fatal(checkErr8)
	}
	if checkErr9 := <-secondDone; !errors.Is(checkErr9, business.ErrStale) {
		t.Fatalf("independent writer did not recheck committed epoch: %v", checkErr9)
	}
	state, err := r.Get(ctx, key)
	if err != nil || state.Connection.Epoch != 2 || len(state.Peers[303].Messages) != 0 {
		t.Fatalf("late write survived: %v", err)
	}
}

func TestTelegramBusinessCipherScopeAndMetadata(t *testing.T) {
	s, r, key, owner := businessStoreFixture(t)
	ctx := context.Background()
	const private = "private-business-content-fixture"
	if checkErr10 := r.Update(ctx, key, func(state *business.State) error {
		state.Peers[303].Messages = []business.Message{{ID: 1, Text: private}}
		state.Peers[303].Summary = &business.Summary{Text: private}
		return nil
	}); checkErr10 != nil {
		t.Fatal(checkErr10)
	}
	row, err := readBusinessRow(ctx, s.db, key)
	if err != nil || row == nil || row.Payload == "" || strings.Contains(row.Metadata+row.Payload, private) {
		t.Fatalf("plaintext or missing ciphertext: %v", err)
	}
	full, err := r.Get(ctx, key)
	if err != nil || full.Peers[303].Messages[0].Text != private {
		t.Fatalf("full read: %v", err)
	}
	metadata, err := r.List(ctx, owner)
	if err != nil || len(metadata) != 1 || metadata[0].Peers[303].Messages[0].Text != "" {
		t.Fatalf("metadata list: %v", err)
	}
	if metadata[0].Peers[303].Summary == nil || metadata[0].Peers[303].Summary.Text != "" {
		t.Fatal("summary content escaped metadata projection")
	}
	if _, checkErr11 := r.List(ctx, ""); !errors.Is(checkErr11, business.ErrDenied) {
		t.Fatalf("empty owner: %v", checkErr11)
	}
	otherKey := key
	otherKey.ConnectionID = "other"
	if checkErr12 := r.Update(ctx, otherKey, func(state *business.State) error {
		state.Connection = full.Connection
		state.Connection.Key = otherKey
		return nil
	}); checkErr12 != nil {
		t.Fatal(checkErr12)
	}
	if _, checkErr13 := s.db.ExecContext(ctx, `UPDATE telegram_business_connections SET payload=? WHERE connection_id=?`, row.Payload, otherKey.ConnectionID); checkErr13 != nil {
		t.Fatal(checkErr13)
	}
	if _, checkErr14 := r.Get(ctx, otherKey); !errors.Is(checkErr14, business.ErrUnavailable) {
		t.Fatalf("scope-substituted ciphertext opened: %v", checkErr14)
	}
}

func TestTelegramBusinessCipherSurvivesAdditiveZeroFieldUpgrade(t *testing.T) {
	s, r, key, _ := businessStoreFixture(t)
	ctx := context.Background()
	if checkErr1 := r.Update(ctx, key, func(state *business.State) error {
		state.Peers[303].Messages = []business.Message{{ID: 1, Text: "retained-before-additive-upgrade"}}
		return nil
	}); checkErr1 != nil {
		t.Fatal(checkErr1)
	}
	state, err := r.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	row, err := readBusinessRow(ctx, s.db, key)
	if err != nil {
		t.Fatal(err)
	}
	removeAddedField := func(raw []byte) []byte {
		var object map[string]json.RawMessage
		if decodeErr := json.Unmarshal(raw, &object); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		var peers map[string]map[string]json.RawMessage
		if decodeErr := json.Unmarshal(object["peers"], &peers); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		delete(peers["303"], "owner_draft_revision")
		peerJSON, marshalErr := json.Marshal(peers)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		object["peers"] = peerJSON
		result, marshalErr := json.Marshal(object)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return result
	}
	row.Metadata = string(removeAddedField([]byte(row.Metadata)))
	full, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	row.Payload, err = r.seal(businessBinding(row), removeAddedField(full))
	if err != nil {
		t.Fatal(err)
	}
	if _, checkErr2 := s.db.ExecContext(ctx, `UPDATE telegram_business_connections SET metadata=?,payload=? WHERE instance_id=? AND bot_id=? AND connection_id=?`, row.Metadata, row.Payload, key.InstanceID, key.BotID, key.ConnectionID); checkErr2 != nil {
		t.Fatal(checkErr2)
	}
	upgraded, err := r.Get(ctx, key)
	if err != nil || upgraded.Peers[303].Messages[0].Text != "retained-before-additive-upgrade" {
		t.Fatalf("additive field broke authenticated history: %v", err)
	}
}

func TestTelegramBusinessMetadataWithoutKeyAndConsent(t *testing.T) {
	s, _, key, _ := businessStoreFixture(t)
	r := NewTelegramBusinessRepository(s, nil)
	ctx := context.Background()
	if checkErr15 := r.Update(ctx, key, func(state *business.State) error { state.Connection.Paused = true; return nil }); checkErr15 != nil {
		t.Fatalf("metadata requires no key: %v", checkErr15)
	}
	if checkErr16 := r.Update(ctx, key, func(state *business.State) error {
		state.Peers[303].Messages = []business.Message{{ID: 1, Text: "private"}}
		return nil
	}); !errors.Is(checkErr16, business.ErrUnavailable) {
		t.Fatalf("content without key: %v", checkErr16)
	}
	crypto, err := config.NewEncryptor(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	r = NewTelegramBusinessRepository(s, crypto)
	if checkErr17 := r.Update(ctx, key, func(state *business.State) error {
		state.Peers[303].Policy.Consent = false
		state.Peers[303].Messages = []business.Message{{ID: 1, Text: "private"}}
		return nil
	}); !errors.Is(checkErr17, business.ErrDenied) {
		t.Fatalf("pre-consent content admitted: %v", checkErr17)
	}
	state, err := r.Get(ctx, key)
	if err != nil || len(state.Peers[303].Messages) != 0 {
		t.Fatalf("failed callback changed state: %v", err)
	}
}

func TestTelegramBusinessOwnerFenceAndCallbackRollback(t *testing.T) {
	s, r, key, owner := businessStoreFixture(t)
	ctx := context.Background()
	stop := errors.New("stop callback")
	count := 0
	if checkErr18 := r.Update(ctx, key, func(state *business.State) error { count++; state.Connection.Epoch = 99; return stop }); !errors.Is(checkErr18, stop) || count != 1 {
		t.Fatalf("callback not once/rollback: %d %v", count, checkErr18)
	}
	state, err := r.Get(ctx, key)
	if err != nil || state.Connection.Epoch != 1 {
		t.Fatalf("callback committed on error: %v", err)
	}
	if checkErr19 := r.Update(ctx, key, func(state *business.State) error { state.Connection.OwnerID = uuid.NewString(); return nil }); !errors.Is(checkErr19, business.ErrDenied) {
		t.Fatalf("owner rebound: %v", checkErr19)
	}
	if actual, checkErr20 := s.ResolveTelegramBusinessOwner(ctx, key.InstanceID, 101); checkErr20 != nil || actual != owner {
		t.Fatalf("owner binding: %v", checkErr20)
	}
	if _, checkErr21 := s.ResolveTelegramBusinessOwner(ctx, "foreign", 101); !errors.Is(checkErr21, business.ErrDenied) {
		t.Fatalf("cross-instance binding: %v", checkErr21)
	}
	if _, checkErr22 := s.db.ExecContext(ctx, `UPDATE users SET disabled=TRUE WHERE id=?`, owner); checkErr22 != nil {
		t.Fatal(checkErr22)
	}
	if _, checkErr23 := r.Get(ctx, key); !errors.Is(checkErr23, business.ErrDenied) {
		t.Fatalf("disabled owner read: %v", checkErr23)
	}
	count = 0
	if checkErr24 := r.Update(ctx, key, func(*business.State) error { count++; return nil }); !errors.Is(checkErr24, business.ErrDenied) || count != 0 {
		t.Fatalf("disabled owner callback invoked: %d %v", count, checkErr24)
	}
}

func TestTelegramBusinessOwnerSecurityEpochCannotResurrect(t *testing.T) {
	s, r, key, owner := businessStoreFixture(t)
	ctx := context.Background()
	if checkErr25 := r.Update(ctx, key, func(state *business.State) error {
		state.Peers[303].Messages = []business.Message{{ID: 1, Text: "old-private"}}
		state.Peers[303].Drafts = map[string]*business.Draft{"draft": {ID: "draft", Scope: business.Scope{Key: key, OwnerID: owner, PeerID: 303}, Text: "old-draft", State: "approved"}}
		return nil
	}); checkErr25 != nil {
		t.Fatal(checkErr25)
	}
	if checkErr26 := s.RevokeUserSessions(ctx, owner); checkErr26 != nil {
		t.Fatal(checkErr26)
	}
	if _, checkErr27 := r.Get(ctx, key); !errors.Is(checkErr27, business.ErrStale) {
		t.Fatalf("old owner authority decrypted: %v", checkErr27)
	}
	list, err := r.List(ctx, owner)
	if err != nil || len(list) != 1 || !list[0].Connection.Quarantined || list[0].Peers[303].Policy.Consent || list[0].Peers[303].Drafts["draft"].State != "cancelled" {
		t.Fatalf("stale metadata not quarantined: %v", err)
	}
	if checkErr28 := r.Update(ctx, key, func(state *business.State) error {
		if !state.Connection.Quarantined || state.Connection.Epoch <= 1 || state.Peers[303].Messages[0].Text != "" || state.Peers[303].Drafts["draft"].Text != "" {
			t.Fatal("callback received stale authority/content")
		}
		return nil
	}); checkErr28 != nil {
		t.Fatal(checkErr28)
	}
	state, err := r.Get(ctx, key)
	if err != nil || !state.Connection.Quarantined || state.Connection.OwnerSecurityEpoch != 2 {
		t.Fatalf("fresh quarantined state: %v", err)
	}
}

func TestTelegramBusinessPollingAtomicLeaseAndCipher(t *testing.T) {
	s, r, key, _ := businessStoreFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if ok, checkErr29 := r.AcquireTelegramBusinessPoll(ctx, key.InstanceID, key.BotID, "holder", now, 2*time.Minute); checkErr29 != nil || !ok {
		t.Fatalf("lease: %v", checkErr29)
	}
	if ok, checkErr30 := r.AcquireTelegramBusinessPoll(ctx, key.InstanceID, key.BotID, "foreign", now, 2*time.Minute); checkErr30 != nil || ok {
		t.Fatalf("competing lease: %v", checkErr30)
	}
	entry := business.PollEntry{UpdateID: 10, Payload: json.RawMessage(`{"update_id":10,"message":{"text":"ordinary-private-fixture"}}`)}
	if checkErr31 := r.StageTelegramBusinessPoll(ctx, key.InstanceID, key.BotID, "foreign", 11, []business.PollEntry{entry}, now); !errors.Is(checkErr31, business.ErrDenied) {
		t.Fatalf("foreign staging: %v", checkErr31)
	}
	if checkErr32 := r.StageTelegramBusinessPoll(ctx, key.InstanceID, key.BotID, "holder", 11, []business.PollEntry{entry}, now); checkErr32 != nil {
		t.Fatal(checkErr32)
	}
	var ciphertext string
	if checkErr33 := s.db.NewRaw(`SELECT payload FROM telegram_business_poll_entries`).Scan(ctx, &ciphertext); checkErr33 != nil || strings.Contains(ciphertext, "ordinary-private-fixture") {
		t.Fatalf("poll plaintext: %v", checkErr33)
	}
	pending, err := r.PendingTelegramBusinessPoll(ctx, key.InstanceID, key.BotID, 100)
	if err != nil || len(pending) != 1 || string(pending[0].Payload) != string(entry.Payload) {
		t.Fatalf("pending: %v", err)
	}
	entry.Payload = json.RawMessage(`{"update_id":10,"business_message":{"text":"forbidden"}}`)
	if checkErr34 := r.StageTelegramBusinessPoll(ctx, key.InstanceID, key.BotID, "holder", 12, []business.PollEntry{entry}, now); !errors.Is(checkErr34, business.ErrDenied) {
		t.Fatalf("raw business staged: %v", checkErr34)
	}
	cursor, err := r.LoadTelegramBusinessPoll(ctx, key.InstanceID, key.BotID)
	if err != nil || cursor.NextOffset != 11 {
		t.Fatalf("failed stage advanced cursor: %v", err)
	}
	if checkErr35 := r.AckTelegramBusinessPoll(ctx, key.InstanceID, key.BotID, 10); checkErr35 != nil {
		t.Fatal(checkErr35)
	}
	if pending, checkErr36 := r.PendingTelegramBusinessPoll(ctx, key.InstanceID, key.BotID, 100); checkErr36 != nil || len(pending) != 0 {
		t.Fatalf("ack did not remove exact receipt: %v", checkErr36)
	}
}

func TestTelegramBusinessBudgetAtomicExpiryAndDailyNoRefund(t *testing.T) {
	s, r, key, owner := businessStoreFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	request := business.BudgetRequest{Scope: business.Scope{Key: key, OwnerID: owner, PeerID: 303}, Provider: "provider", InputTokens: 100, OutputTokens: 100, CostMicros: 100, At: now, ExpiresAt: now.Add(5 * time.Minute)}
	first, err := r.Reserve(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, checkErr37 := r.Reserve(ctx, request); !errors.Is(checkErr37, business.ErrQuota) {
		t.Fatalf("peer concurrency bypass: %v", checkErr37)
	}
	if checkErr38 := r.Release(ctx, first); checkErr38 != nil {
		t.Fatal(checkErr38)
	}
	for i := 1; i < 10; i++ {
		reservation, err := r.Reserve(ctx, request)
		if err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		if checkErr39 := r.Release(ctx, reservation); checkErr39 != nil {
			t.Fatal(checkErr39)
		}
	}
	if _, checkErr40 := r.Reserve(ctx, request); !errors.Is(checkErr40, business.ErrQuota) {
		t.Fatalf("release refunded daily attempts: %v", checkErr40)
	}
	var attempts int
	if checkErr41 := s.db.NewRaw(`SELECT attempts FROM telegram_business_budget_usage WHERE scope_key=?`, businessBudgetScopes(request.Scope)[0].Key).Scan(ctx, &attempts); checkErr41 != nil || attempts != 10 {
		t.Fatalf("failed reservation partially charged: %d %v", attempts, checkErr41)
	}
	request.At = request.At.Add(24 * time.Hour)
	request.ExpiresAt = request.At.Add(5 * time.Minute)
	if _, checkErr42 := r.Reserve(ctx, request); checkErr42 != nil {
		t.Fatalf("next UTC day unavailable: %v", checkErr42)
	}
	request.At = request.At.Add(6 * time.Minute)
	request.ExpiresAt = request.At.Add(5 * time.Minute)
	if _, checkErr43 := r.Reserve(ctx, request); checkErr43 != nil {
		t.Fatalf("expired concurrency not reclaimed: %v", checkErr43)
	}
}

func TestTelegramBusinessBudgetCrossPeerAndCostRollback(t *testing.T) {
	s, r, key, owner := businessStoreFixture(t)
	ctx := context.Background()
	if checkErr44 := r.Update(ctx, key, func(state *business.State) error {
		for _, id := range []int64{304, 305} {
			state.Peers[id] = &business.Peer{ID: id, Policy: state.Peers[303].Policy}
		}
		return nil
	}); checkErr44 != nil {
		t.Fatal(checkErr44)
	}
	now := time.Now().UTC()
	req := business.BudgetRequest{Scope: business.Scope{Key: key, OwnerID: owner, PeerID: 303}, Provider: "provider", InputTokens: 100, OutputTokens: 100, CostMicros: 100, At: now, ExpiresAt: now.Add(5 * time.Minute)}
	first, err := r.Reserve(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.Scope.PeerID = 304
	second, err := r.Reserve(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.Scope.PeerID = 305
	if _, checkErr45 := r.Reserve(ctx, req); !errors.Is(checkErr45, business.ErrQuota) {
		t.Fatalf("connection concurrency bypassed: %v", checkErr45)
	}
	var peerAttempts int
	if checkErr46 := s.db.NewRaw(`SELECT COUNT(*) FROM telegram_business_budget_usage WHERE scope_key=?`, businessBudgetScopes(req.Scope)[0].Key).Scan(ctx, &peerAttempts); checkErr46 != nil || peerAttempts != 0 {
		t.Fatalf("failed late-scope reservation charged peer: %v", checkErr46)
	}
	for _, reservation := range []business.Reservation{first, second} {
		if checkErr47 := r.Release(ctx, reservation); checkErr47 != nil {
			t.Fatal(checkErr47)
		}
	}
	req.CostMicros = 1000001
	if _, checkErr48 := r.Reserve(ctx, req); !errors.Is(checkErr48, business.ErrQuota) {
		t.Fatalf("connection monetary cap bypassed: %v", checkErr48)
	}
}

func TestTelegramBusinessTokenOnlyCannotWeakenAccountBudgetPolicy(t *testing.T) {
	_, r, key, owner := businessStoreFixture(t)
	ctx := context.Background()
	if checkErr1 := r.Update(ctx, key, func(state *business.State) error { state.Peers[303].Policy.TokenOnly = true; return nil }); checkErr1 != nil {
		t.Fatal(checkErr1)
	}
	now := time.Now().UTC()
	request := business.BudgetRequest{Scope: business.Scope{Key: key, OwnerID: owner, PeerID: 303}, Provider: "provider", InputTokens: 100, OutputTokens: 100, TokenOnly: true, At: now, ExpiresAt: now.Add(5 * time.Minute)}
	if _, checkErr2 := r.Reserve(ctx, request); !errors.Is(checkErr2, business.ErrDenied) {
		t.Fatalf("peer weakened account monetary policy: %v", checkErr2)
	}
	if checkErr3 := r.Update(ctx, key, func(state *business.State) error { state.Connection.Policy.TokenOnly = true; return nil }); checkErr3 != nil {
		t.Fatal(checkErr3)
	}
	if _, checkErr4 := r.Reserve(ctx, request); checkErr4 != nil {
		t.Fatalf("explicit account+peer acknowledgement rejected: %v", checkErr4)
	}
}

func TestTelegramBusinessPollQueueCapRollsBackCursor(t *testing.T) {
	_, r, key, _ := businessStoreFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if ok, checkErr49 := r.AcquireTelegramBusinessPoll(ctx, key.InstanceID, key.BotID, "holder", now, 2*time.Minute); checkErr49 != nil || !ok {
		t.Fatalf("acquire: %v", checkErr49)
	}
	for batch := 0; batch < 10; batch++ {
		entries := make([]business.PollEntry, 100)
		for i := range entries {
			id := int64(batch*100 + i + 1)
			entries[i] = business.PollEntry{UpdateID: id, Payload: json.RawMessage(fmt.Sprintf(`{"update_id":%d,"message":{"text":"fixture"}}`, id))}
		}
		if checkErr50 := r.StageTelegramBusinessPoll(ctx, key.InstanceID, key.BotID, "holder", int64((batch+1)*100+1), entries, now); checkErr50 != nil {
			t.Fatalf("batch %d: %v", batch, checkErr50)
		}
	}
	entry := business.PollEntry{UpdateID: 1001, Payload: json.RawMessage(`{"update_id":1001,"message":{"text":"fixture"}}`)}
	if checkErr51 := r.StageTelegramBusinessPoll(ctx, key.InstanceID, key.BotID, "holder", 1002, []business.PollEntry{entry}, now); !errors.Is(checkErr51, business.ErrQuota) {
		t.Fatalf("queue capacity bypassed: %v", checkErr51)
	}
	state, err := r.LoadTelegramBusinessPoll(ctx, key.InstanceID, key.BotID)
	if err != nil || state.NextOffset != 1001 {
		t.Fatalf("failed admission advanced cursor: %v", err)
	}
}

func TestTelegramBusinessRetentionDropsContentPreservesUnknown(t *testing.T) {
	s, r, key, owner := businessStoreFixture(t)
	ctx := context.Background()
	if checkErr52 := r.Update(ctx, key, func(state *business.State) error {
		state.Peers[303].Messages = []business.Message{{ID: 1, Text: "expired-content"}}
		state.Peers[303].Actions = map[string]*business.Action{"unknown": {ID: "unknown", Scope: business.Scope{Key: key, OwnerID: owner, PeerID: 303}, State: "unknown", Automatic: true, Params: business.Params{"text": json.RawMessage(`"expired-content"`)}}}
		state.AccountActions = map[string]*business.Action{"stars": {ID: "stars", Scope: business.Scope{Key: key, OwnerID: owner}, State: "unknown", ReservedStars: 100, Params: business.Params{"star_count": json.RawMessage(`100`)}}}
		return nil
	}); checkErr52 != nil {
		t.Fatal(checkErr52)
	}
	now := time.Now().UTC()
	if _, checkErr53 := s.db.ExecContext(ctx, `UPDATE telegram_business_connections SET updated_at=?`, now.Add(-8*24*time.Hour)); checkErr53 != nil {
		t.Fatal(checkErr53)
	}
	if checkErr54 := r.PurgeTelegramBusiness(ctx, now); checkErr54 != nil {
		t.Fatal(checkErr54)
	}
	state, err := r.Get(ctx, key)
	if err != nil || state.Peers[303].Messages[0].Text != "" || state.Peers[303].Actions["unknown"].State != "unknown" || len(state.Peers[303].Actions["unknown"].Params) != 0 {
		t.Fatalf("retention lost tombstone or retained content: %v", err)
	}
	if !state.Peers[303].Actions["unknown"].Automatic || state.AccountActions["stars"].ReservedStars != 100 || len(state.AccountActions["stars"].Params) != 0 {
		t.Fatal("privacy purge refunded minimal automatic/financial ledger")
	}
}

func TestTelegramBusinessDeletedScopeErasureWorksAfterOwnerDisable(t *testing.T) {
	s, r, key, owner := businessStoreFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if checkErr55 := r.Update(ctx, key, func(state *business.State) error {
		peer := state.Peers[303]
		peer.Messages = []business.Message{{ID: 1, Date: now, Text: "deleted-source"}}
		peer.Summary = &business.Summary{Text: "derived-summary", CreatedAt: now}
		peer.Drafts = map[string]*business.Draft{"draft": {ID: "draft", Scope: business.Scope{Key: key, OwnerID: owner, PeerID: 303}, Text: "derived-draft", State: "approved", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}}
		peer.Actions = map[string]*business.Action{"action": {ID: "action", Scope: business.Scope{Key: key, OwnerID: owner, PeerID: 303}, Params: business.Params{"text": json.RawMessage(`"derived-action"`)}, State: "dispatching", CreatedAt: now}}
		state.Peers[304] = &business.Peer{ID: 304, Policy: peer.Policy, Messages: []business.Message{{ID: 1, Date: now, Text: "unrelated-peer"}}}
		return nil
	}); checkErr55 != nil {
		t.Fatal(checkErr55)
	}
	if _, checkErr56 := s.db.ExecContext(ctx, `UPDATE users SET disabled=TRUE WHERE id=?`, owner); checkErr56 != nil {
		t.Fatal(checkErr56)
	}
	if routed, checkErr57 := s.TelegramBusinessIngressOwner(ctx, key); checkErr57 != nil || routed != owner {
		t.Fatalf("cleanup owner routing: %v", checkErr57)
	}
	if checkErr58 := r.ApplyTelegramBusinessDeletion(ctx, key, "foreign", 303, []int{1}, now); !errors.Is(checkErr58, business.ErrDenied) {
		t.Fatalf("foreign cleanup accepted: %v", checkErr58)
	}
	if checkErr59 := r.ApplyTelegramBusinessDeletion(ctx, key, owner, 303, []int{1}, now); checkErr59 != nil {
		t.Fatal(checkErr59)
	}
	row, err := readBusinessRow(ctx, s.db, key)
	if err != nil {
		t.Fatal(err)
	}
	state, err := r.decodeForScope(ctx, s.db, row, true, owner)
	if err != nil {
		t.Fatal(err)
	}
	peer := state.Peers[303]
	if !peer.Messages[0].Deleted || peer.Messages[0].Text != "" || peer.Summary != nil || peer.Drafts["draft"].Text != "" || len(peer.Actions["action"].Params) != 0 || peer.Actions["action"].State != "unknown" || state.Peers[304].Messages[0].Text != "unrelated-peer" {
		t.Fatal("cleanup retained derived content or crossed peer scope")
	}
	if _, checkErr60 := r.Get(ctx, key); !errors.Is(checkErr60, business.ErrDenied) {
		t.Fatalf("cleanup restored disabled user authority: %v", checkErr60)
	}
	revision := peer.Revision
	if checkErr61 := r.ApplyTelegramBusinessDeletion(ctx, key, owner, 303, []int{1}, now); checkErr61 != nil {
		t.Fatal(checkErr61)
	}
	row, err = readBusinessRow(ctx, s.db, key)
	if err != nil {
		t.Fatal(err)
	}
	state, err = r.decodeForScope(ctx, s.db, row, true, owner)
	if err != nil || state.Peers[303].Revision != revision {
		t.Fatalf("duplicate deletion invalidated later context: %v", err)
	}
}

func TestTelegramBusinessPollingLeaseIsBotWide(t *testing.T) {
	ctx := context.Background()
	_, repo, key, _ := businessStoreFixture(t)
	now := time.Now().UTC()
	acquire := func(instance, holder string, at time.Time, want bool) {
		t.Helper()
		got, err := repo.AcquireTelegramBusinessPoll(ctx, instance, key.BotID, holder, at, time.Minute)
		if err != nil || got != want {
			t.Fatalf("instance %s acquire = %v, %v; want %v", instance, got, err, want)
		}
	}
	acquire(key.InstanceID, "first", now, true)
	acquire("other-instance", "second", now, false)
	// Reusing a holder string cannot create an independent second cursor.
	acquire("other-instance", "first", now, false)
	if checkErr1 := repo.ReleaseTelegramBusinessPoll(ctx, key.InstanceID, key.BotID, "foreign"); checkErr1 != nil {
		t.Fatal(checkErr1)
	}
	acquire("other-instance", "second", now, false)
	if checkErr2 := repo.ReleaseTelegramBusinessPoll(ctx, key.InstanceID, key.BotID, "first"); checkErr2 != nil {
		t.Fatal(checkErr2)
	}
	acquire("other-instance", "second", now, true)
	acquire(key.InstanceID, "first", now, false)
	// A crashed holder's expired lease permits takeover without replay authority.
	acquire(key.InstanceID, "first", now.Add(2*time.Minute), true)
	acquire("other-instance", "second", now.Add(2*time.Minute), false)
}

func TestTelegramBusinessProcessingMetadataUsesClosedCodes(t *testing.T) {
	ctx := context.Background()
	store, repo, key, owner := businessStoreFixture(t)
	processedAt := time.Now().UTC().Truncate(time.Second)
	if updateErr := repo.Update(ctx, key, func(st *business.State) error {
		peer := st.Peers[303]
		peer.LastProcessingState = "failed"
		peer.LastProcessingReason = "budget_exhausted"
		peer.LastProcessedAt = processedAt
		return nil
	}); updateErr != nil {
		t.Fatal(updateErr)
	}
	// Metadata works without decryption and never needs model/provider error text.
	metadataOnly := NewTelegramBusinessRepository(store, nil)
	states, err := metadataOnly.List(ctx, owner)
	if err != nil || len(states) != 1 {
		t.Fatalf("metadata read %d, %v", len(states), err)
	}
	peer := states[0].Peers[303]
	if peer.LastProcessingState != "failed" || peer.LastProcessingReason != "budget_exhausted" || !peer.LastProcessedAt.Equal(processedAt) {
		t.Fatalf("processing metadata lost: %+v", peer)
	}
	for _, field := range []string{"state", "reason"} {
		t.Run(field, func(t *testing.T) {
			if updateErr := repo.Update(ctx, key, func(st *business.State) error {
				if field == "state" {
					st.Peers[303].LastProcessingState = "private message body"
				} else {
					st.Peers[303].LastProcessingReason = "provider error containing private content"
				}
				return nil
			}); !errors.Is(updateErr, business.ErrDenied) {
				t.Fatalf("arbitrary plaintext processing field accepted: %v", updateErr)
			}
		})
	}
	states, err = metadataOnly.List(ctx, owner)
	if err != nil || len(states) != 1 || states[0].Peers[303].LastProcessingReason != "budget_exhausted" {
		t.Fatal("rejected processing update did not roll back", err)
	}
}

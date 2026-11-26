package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"time"

	business "github.com/iulita-ai/iulita/internal/telegrambusiness"
	"github.com/uptrace/bun"
)

const maxBusinessStateBytes = 2 << 20

// TelegramBusinessCrypto requires an explicitly enabled authenticated vault.
// Its methods must be bounded local operations, never network requests.
type TelegramBusinessCrypto interface {
	EncryptionEnabled() bool
	Encrypt(string) (string, error)
	Decrypt(string) (string, error)
}

// TelegramBusinessRepository persists isolated connected-account snapshots.
// No business content is written to ordinary chat, facts, indexes or jobs.
type TelegramBusinessRepository struct {
	store  *Store
	crypto TelegramBusinessCrypto
}

// NewTelegramBusinessRepository permits metadata-only operation without a vault.
func NewTelegramBusinessRepository(store *Store, crypto TelegramBusinessCrypto) *TelegramBusinessRepository {
	return &TelegramBusinessRepository{store: store, crypto: crypto}
}

// EnsureTelegramBusiness adds only isolated business tables and indexes.
func (s *Store) EnsureTelegramBusiness(ctx context.Context) error {
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS telegram_business_connections (instance_id TEXT NOT NULL, bot_id BIGINT NOT NULL, connection_id TEXT NOT NULL, owner_id TEXT NOT NULL, revision BIGINT NOT NULL, metadata TEXT NOT NULL, payload TEXT NOT NULL, updated_at DATETIME NOT NULL, PRIMARY KEY(instance_id,bot_id,connection_id))`,
		`CREATE INDEX IF NOT EXISTS telegram_business_owner ON telegram_business_connections(owner_id,instance_id,bot_id,connection_id)`,
		`CREATE TABLE IF NOT EXISTS telegram_business_poll (instance_id TEXT NOT NULL, bot_id BIGINT NOT NULL, next_offset BIGINT NOT NULL DEFAULT 0, updated_at DATETIME NOT NULL, lease_owner TEXT NOT NULL DEFAULT '', lease_until DATETIME NOT NULL, PRIMARY KEY(instance_id,bot_id))`,
		`CREATE TABLE IF NOT EXISTS telegram_business_poll_entries (instance_id TEXT NOT NULL, bot_id BIGINT NOT NULL, update_id BIGINT NOT NULL, payload TEXT NOT NULL, created_at DATETIME NOT NULL, PRIMARY KEY(instance_id,bot_id,update_id))`,
		`CREATE TABLE IF NOT EXISTS telegram_business_budget_usage (scope_key TEXT NOT NULL, day TEXT NOT NULL, attempts BIGINT NOT NULL, input_tokens BIGINT NOT NULL, output_tokens BIGINT NOT NULL, cost_micros BIGINT NOT NULL, PRIMARY KEY(scope_key,day))`,
		`CREATE TABLE IF NOT EXISTS telegram_business_budget_reservations (id TEXT PRIMARY KEY, scope_keys TEXT NOT NULL, expires_at DATETIME NOT NULL, released BOOLEAN NOT NULL DEFAULT FALSE)`,
		`CREATE INDEX IF NOT EXISTS telegram_business_budget_expiry ON telegram_business_budget_reservations(expires_at,released)`,
		`CREATE TABLE IF NOT EXISTS telegram_business_source_usage (scope_key TEXT NOT NULL, day TEXT NOT NULL, calls BIGINT NOT NULL, duration_seconds BIGINT NOT NULL, bytes BIGINT NOT NULL, PRIMARY KEY(scope_key,day))`,
	} {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("creating business storage: %w", err)
		}
	}
	return nil
}

func validBusinessKey(key business.Key) bool {
	return key.InstanceID != "" && len(key.InstanceID) <= 256 && key.BotID > 0 && key.ConnectionID != "" && len(key.ConnectionID) <= 512
}

// ResolveTelegramBusinessOwner resolves only an existing exact instance binding.
func (s *Store) ResolveTelegramBusinessOwner(ctx context.Context, instance string, telegramID int64) (string, error) {
	if instance == "" || telegramID <= 0 {
		return "", business.ErrDenied
	}
	return businessOwner(ctx, s.db, instance, telegramID)
}

func businessOwner(ctx context.Context, db bun.IDB, instance string, telegramID int64) (string, error) {
	var owners []string
	err := db.NewRaw(`SELECT DISTINCT u.id FROM users u JOIN user_channels c ON c.user_id=u.id WHERE c.channel_instance_id=? AND c.channel_type='telegram' AND c.channel_user_id=? AND c.enabled=TRUE AND u.disabled=FALSE LIMIT 2`, instance, strconv.FormatInt(telegramID, 10)).Scan(ctx, &owners)
	if err != nil {
		return "", err
	}
	if len(owners) != 1 || owners[0] == "" {
		return "", business.ErrDenied
	}
	return owners[0], nil
}

func checkBusinessOwner(ctx context.Context, db bun.IDB, c business.Connection) error {
	if c.OwnerID == "" {
		return nil // Unbound discovery is metadata-only; it conveys no authority.
	}
	owner, err := businessOwner(ctx, db, c.Key.InstanceID, c.OwnerTelegramID)
	if err != nil {
		return err
	}
	if owner != c.OwnerID {
		return business.ErrDenied
	}
	return nil
}

func businessOwnerEpoch(ctx context.Context, db bun.IDB, c business.Connection) (uint64, error) {
	if err := checkBusinessOwner(ctx, db, c); err != nil {
		return 0, err
	}
	if c.OwnerID == "" {
		return 0, nil
	}
	var epoch uint64
	err := db.NewRaw(`SELECT security_epoch FROM users WHERE id=? AND disabled=FALSE`, c.OwnerID).Scan(ctx, &epoch)
	return epoch, err
}

func quarantineBusinessState(state *business.State, ownerEpoch uint64) error {
	if state.Connection.Epoch == math.MaxUint64 {
		return business.ErrQuota
	}
	state.Connection.Epoch++
	state.Connection.OwnerSecurityEpoch = ownerEpoch
	state.Connection.Quarantined = true
	state.Connection.Policy.Consent = false
	state.Connection.Policy.Enabled = false
	state.Connection.Policy.Mode = business.ModeOff
	for _, peer := range state.Peers {
		if peer == nil {
			return business.ErrUnavailable
		}
		peer.Policy.Consent = false
		peer.Policy.Enabled = false
		peer.Policy.Mode = business.ModeOff
		peer.Summary = nil
		for _, draft := range peer.Drafts {
			if draft != nil {
				draft.State = "cancelled"
			}
		}
		for _, action := range peer.Actions {
			cancelBusinessAction(action)
		}
	}
	for _, action := range state.AccountActions {
		cancelBusinessAction(action)
	}
	return nil
}

func cancelBusinessAction(action *business.Action) {
	if action == nil {
		return
	}
	if action.State == "dispatching" || action.State == "sending" {
		action.State = "unknown"
	} else if action.State != "sent" && action.State != "unknown" {
		action.State = "cancelled"
	}
}

type businessStateRow struct {
	InstanceID   string    `bun:"instance_id"`
	BotID        int64     `bun:"bot_id"`
	ConnectionID string    `bun:"connection_id"`
	OwnerID      string    `bun:"owner_id"`
	Revision     int64     `bun:"revision"`
	Metadata     string    `bun:"metadata"`
	Payload      string    `bun:"payload"`
	UpdatedAt    time.Time `bun:"updated_at"`
}

func readBusinessRow(ctx context.Context, db bun.IDB, key business.Key) (*businessStateRow, error) {
	row := new(businessStateRow)
	err := db.NewRaw(`SELECT * FROM telegram_business_connections WHERE instance_id=? AND bot_id=? AND connection_id=?`, key.InstanceID, key.BotID, key.ConnectionID).Scan(ctx, row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return row, err
}

type businessSealedEnvelope struct {
	Version int             `json:"version"`
	Binding string          `json:"binding"`
	Value   json.RawMessage `json:"value"`
}

func (r *TelegramBusinessRepository) seal(binding string, value []byte) (string, error) {
	if binding == "" || r.crypto == nil || !r.crypto.EncryptionEnabled() {
		return "", business.ErrUnavailable
	}
	encoded, err := json.Marshal(businessSealedEnvelope{Version: 1, Binding: binding, Value: value})
	if err != nil {
		return "", business.ErrUnavailable
	}
	sealed, err := r.crypto.Encrypt(string(encoded))
	if err != nil || sealed == "" {
		return "", business.ErrUnavailable
	}
	return sealed, nil
}

func (r *TelegramBusinessRepository) open(binding, ciphertext string) ([]byte, error) {
	if binding == "" || r.crypto == nil || !r.crypto.EncryptionEnabled() {
		return nil, business.ErrUnavailable
	}
	plaintext, err := r.crypto.Decrypt(ciphertext)
	if err != nil || len(plaintext) > 3*maxBusinessStateBytes {
		return nil, business.ErrUnavailable
	}
	var envelope businessSealedEnvelope
	if json.Unmarshal([]byte(plaintext), &envelope) != nil || envelope.Version != 1 || envelope.Binding != binding {
		return nil, business.ErrUnavailable
	}
	return envelope.Value, nil
}

func businessBinding(row *businessStateRow) string {
	metadataHash := sha256.Sum256([]byte(row.Metadata))
	bound, err := json.Marshal([]any{"business-state-v1", row.InstanceID, row.BotID, row.ConnectionID, row.OwnerID, row.Revision, hex.EncodeToString(metadataHash[:])})
	if err != nil {
		return "" // An unavailable binding is rejected before sealing/opening.
	}
	return string(bound)
}

// businessMetadata is a positive field allowlist. New private domain fields are
// encrypted by default instead of silently entering the clear metadata column.
func businessMetadata(state *business.State) (*business.State, error) {
	encoded, err := json.Marshal(state)
	if err != nil || len(encoded) > maxBusinessStateBytes {
		return nil, business.ErrQuota
	}
	c := state.Connection
	metadata := &business.State{
		Connection: business.Connection{
			RemoteConnection: business.RemoteConnection{Key: c.Key, OwnerTelegramID: c.OwnerTelegramID, OwnerChatID: c.OwnerChatID, Enabled: c.Enabled, Rights: c.Rights, ObservedAt: c.ObservedAt},
			OwnerID:          c.OwnerID, Epoch: c.Epoch, ControlUpdateID: c.ControlUpdateID, DeliveryGeneration: c.DeliveryGeneration,
			VerifiedAt: c.VerifiedAt, Paused: c.Paused, RestoreGeneration: c.RestoreGeneration, Quarantined: c.Quarantined,
			Policy: businessPolicyMetadata(c.Policy), DispatchActionID: c.DispatchActionID, OwnerSecurityEpoch: c.OwnerSecurityEpoch,
			RuntimeGeneration: c.RuntimeGeneration, NeedsRefresh: c.NeedsRefresh,
		},
	}
	if state.Seen != nil {
		metadata.Seen = make(map[string]time.Time, len(state.Seen))
		for id, at := range state.Seen {
			metadata.Seen[id] = at
		}
	}
	if state.Audit != nil {
		metadata.Audit = make([]business.Audit, 0, len(state.Audit))
		for _, event := range state.Audit {
			metadata.Audit = append(metadata.Audit, business.Audit{At: event.At, Kind: event.Kind, OperationID: event.OperationID, Outcome: event.Outcome})
		}
	}
	if state.AccountActions != nil {
		metadata.AccountActions = make(map[string]*business.Action, len(state.AccountActions))
		for id, action := range state.AccountActions {
			metadata.AccountActions[id] = businessActionMetadata(action)
		}
	}
	if state.Peers != nil {
		metadata.Peers = make(map[int64]*business.Peer, len(state.Peers))
	}
	for id, p := range state.Peers {
		if p == nil {
			return nil, business.ErrDenied
		}
		peer := &business.Peer{ID: p.ID, Policy: businessPolicyMetadata(p.Policy), Revision: p.Revision, LastIncomingAt: p.LastIncomingAt, ReplayAfter: p.ReplayAfter, AutoRevision: p.AutoRevision, AutoClaim: p.AutoClaim, OwnerDraftRevision: p.OwnerDraftRevision, ProcessRevision: p.ProcessRevision}
		peer.LastMediaOmitted = p.LastMediaOmitted
		// Only closed content-free processing codes belong in plaintext metadata.
		// Arbitrary provider/error text remains forbidden here.
		switch p.LastProcessingState {
		case "", "processing", "ready", "failed", "skipped":
		default:
			return nil, business.ErrDenied
		}
		switch p.LastProcessingReason {
		case "", "draft_ready", "sent", "budget_exhausted", "stale_context", "cancelled", "unavailable", "unsupported", "forbidden", "unknown_outcome", "processing_failed":
		default:
			return nil, business.ErrDenied
		}
		peer.LastProcessingState = p.LastProcessingState
		peer.LastProcessingReason = p.LastProcessingReason
		peer.LastProcessedAt = p.LastProcessedAt
		if p.Messages != nil {
			peer.Messages = make([]business.Message, 0, len(p.Messages))
			for i := range p.Messages {
				m := &p.Messages[i]
				peer.Messages = append(peer.Messages, business.Message{ID: m.ID, SenderID: m.SenderID, SenderIsBot: m.SenderIsBot, Date: m.Date, Revision: m.Revision, Direction: m.Direction, Deleted: m.Deleted})
			}
		}
		if p.Drafts != nil {
			peer.Drafts = make(map[string]*business.Draft, len(p.Drafts))
			for id, d := range p.Drafts {
				if d != nil {
					peer.Drafts[id] = &business.Draft{ID: d.ID, Scope: d.Scope, Epoch: d.Epoch, ContextRevision: d.ContextRevision, Revision: d.Revision, CreatedAt: d.CreatedAt, ExpiresAt: d.ExpiresAt, State: d.State, ActionID: d.ActionID}
				} else {
					peer.Drafts[id] = nil
				}
			}
		}
		if p.Actions != nil {
			peer.Actions = make(map[string]*business.Action, len(p.Actions))
			for id, a := range p.Actions {
				peer.Actions[id] = businessActionMetadata(a)
			}
		}
		if p.Summary != nil {
			v := p.Summary
			peer.Summary = &business.Summary{ThroughID: v.ThroughID, ThroughBytes: v.ThroughBytes, CreatedAt: v.CreatedAt, BeforeBytes: v.BeforeBytes, AfterBytes: v.AfterBytes}
			if v.Sources != nil {
				peer.Summary.Sources = make([]business.SourceRevision, 0, len(v.Sources))
				for _, ref := range v.Sources {
					peer.Summary.Sources = append(peer.Summary.Sources, business.SourceRevision{ID: ref.ID, Revision: ref.Revision})
				}
			}
		}
		metadata.Peers[id] = peer
	}
	return metadata, nil
}

func businessPolicyMetadata(p business.PeerPolicy) business.PeerPolicy {
	methods := p.AllowedMethods
	if methods != nil {
		methods = append(make([]business.Method, 0, len(methods)), methods...)
	}
	out := business.PeerPolicy{Enabled: p.Enabled, Consent: p.Consent, Mode: p.Mode, Provider: p.Provider, Revision: p.Revision, AllowedMethods: methods, AutoReply: p.AutoReply, TokenOnly: p.TokenOnly}
	out.MediaConsent = p.MediaConsent
	out.MediaProvider = p.MediaProvider
	if p.AutoRules != nil {
		rules := p.AutoRules
		out.AutoRules = &business.AutoRules{Timezone: rules.Timezone, StartHour: rules.StartHour, EndHour: rules.EndHour, MaxRepliesPerDay: rules.MaxRepliesPerDay}
		if rules.Classes != nil {
			out.AutoRules.Classes = append(make([]string, 0, len(rules.Classes)), rules.Classes...)
		}
	}
	return out
}

func businessActionMetadata(a *business.Action) *business.Action {
	if a == nil {
		return nil
	}
	out := &business.Action{ID: a.ID, Scope: a.Scope, Epoch: a.Epoch, ContextRevision: a.ContextRevision, PolicyRevision: a.PolicyRevision, Method: a.Method, Digest: a.Digest, State: a.State, ApprovalSteps: a.ApprovalSteps, RequiredSteps: a.RequiredSteps, CreatedAt: a.CreatedAt, ExpiresAt: a.ExpiresAt, DraftID: a.DraftID, DraftRevision: a.DraftRevision, SourceEpoch: a.SourceEpoch}
	if a.SourceKey != nil {
		key := *a.SourceKey
		out.SourceKey = &key
	}
	out.ReservedStars = a.ReservedStars
	out.Automatic = a.Automatic
	out.DispatchAt = a.DispatchAt
	if a.Receipt != nil {
		out.Receipt = &business.Receipt{StoryID: a.Receipt.StoryID}
		if a.Receipt.MessageIDs != nil {
			out.Receipt.MessageIDs = append(make([]int, 0, len(a.Receipt.MessageIDs)), a.Receipt.MessageIDs...)
		}
	}
	return out
}

func unconsentedBusinessContent(peer, prior *business.Peer) bool {
	if !peer.Policy.MediaConsent {
		for i := range peer.Messages {
			message := &peer.Messages[i]
			if message.ExtractedText == "" && message.ExtractedProvider == "" {
				continue
			}
			found := false
			if prior != nil {
				for j := range prior.Messages {
					old := &prior.Messages[j]
					if old.ID == message.ID && old.ExtractedText == message.ExtractedText && old.ExtractedProvider == message.ExtractedProvider {
						found = true
						break
					}
				}
			}
			if !found {
				return true
			}
		}
	}
	if peer.Policy.Consent {
		return false
	}
	if peer.Summary != nil && peer.Summary.Text != "" && (prior == nil || prior.Summary == nil || prior.Summary.Text != peer.Summary.Text) {
		return true
	}
	for i := range peer.Messages {
		message := &peer.Messages[i]
		if message.Text == "" && len(message.Media) == 0 && message.MediaGroupID == "" && message.ExtractedText == "" && message.ExtractedProvider == "" {
			continue
		}
		found := false
		if prior != nil {
			for j := range prior.Messages {
				old := &prior.Messages[j]
				if old.ID == message.ID && old.Text == message.Text && old.MediaGroupID == message.MediaGroupID && old.ExtractedText == message.ExtractedText && old.ExtractedProvider == message.ExtractedProvider && reflect.DeepEqual(old.Media, message.Media) {
					found = true
					break
				}
			}
		}
		if !found {
			return true
		}
	}
	for id, draft := range peer.Drafts {
		if draft != nil && draft.Text != "" && (prior == nil || prior.Drafts[id] == nil || prior.Drafts[id].Text != draft.Text) {
			return true
		}
	}
	for id, action := range peer.Actions {
		if action == nil {
			continue
		}
		var old *business.Action
		if prior != nil {
			old = prior.Actions[id]
		}
		if len(action.Params) > 0 && (old == nil || !reflect.DeepEqual(action.Params, old.Params)) {
			return true
		}
		if action.Receipt != nil && len(action.Receipt.Result) > 0 && (old == nil || old.Receipt == nil || !reflect.DeepEqual(action.Receipt.Result, old.Receipt.Result)) {
			return true
		}
	}
	return false
}

func (r *TelegramBusinessRepository) decode(ctx context.Context, db bun.IDB, row *businessStateRow, full bool) (*business.State, error) {
	return r.decodeForScope(ctx, db, row, full, "")
}

func (r *TelegramBusinessRepository) decodeForScope(ctx context.Context, db bun.IDB, row *businessStateRow, full bool, cleanupOwner string) (*business.State, error) {
	var state business.State
	if len(row.Metadata) > maxBusinessStateBytes || json.Unmarshal([]byte(row.Metadata), &state) != nil || state.Connection.Key != (business.Key{InstanceID: row.InstanceID, BotID: row.BotID, ConnectionID: row.ConnectionID}) || state.Connection.OwnerID != row.OwnerID {
		return nil, business.ErrUnavailable
	}
	if cleanupOwner != "" {
		if state.Connection.OwnerID != cleanupOwner {
			return nil, business.ErrDenied
		}
	} else {
		ownerEpoch, err := businessOwnerEpoch(ctx, db, state.Connection)
		if err != nil {
			return nil, err
		}
		if state.Connection.OwnerID != "" && ownerEpoch != state.Connection.OwnerSecurityEpoch {
			if full {
				return nil, business.ErrStale // Do not decrypt stale owner authority.
			}
			if err := quarantineBusinessState(&state, ownerEpoch); err != nil {
				return nil, err
			}
			return &state, nil
		}
	}
	if full && row.Payload != "" {
		// Authenticate the original stored metadata bytes in the envelope, then
		// compare typed values. Added zero-valued domain fields must not make a
		// previously valid ciphertext unreadable after an additive upgrade.
		normalizedMetadata, normalizeErr := json.Marshal(&state)
		if normalizeErr != nil {
			return nil, business.ErrUnavailable
		}
		encoded, err := r.open(businessBinding(row), row.Payload)
		if err != nil || json.Unmarshal(encoded, &state) != nil {
			return nil, business.ErrUnavailable
		}
		projection, err := businessMetadata(&state)
		if err != nil {
			return nil, err
		}
		projected, err := json.Marshal(projection)
		if err != nil || !bytes.Equal(projected, normalizedMetadata) {
			return nil, business.ErrUnavailable
		}
	}
	return &state, nil
}

// Get resolves an exact connection and opens content only after owner checks.
func (r *TelegramBusinessRepository) Get(ctx context.Context, key business.Key) (*business.State, error) {
	if !validBusinessKey(key) {
		return nil, business.ErrDenied
	}
	row, err := readBusinessRow(ctx, r.store.db, key)
	if err != nil || row == nil {
		return nil, err
	}
	return r.decode(ctx, r.store.db, row, true)
}

// List returns at most 100 metadata-only connections for one active owner.
func (r *TelegramBusinessRepository) List(ctx context.Context, owner string) ([]business.State, error) {
	if owner == "" {
		return nil, business.ErrDenied
	}
	var rows []businessStateRow
	if err := r.store.db.NewRaw(`SELECT * FROM telegram_business_connections WHERE owner_id=? ORDER BY instance_id,bot_id,connection_id LIMIT 101`, owner).Scan(ctx, &rows); err != nil {
		return nil, err
	}
	if len(rows) > 100 {
		return nil, business.ErrQuota
	}
	states := make([]business.State, 0, len(rows))
	for i := range rows {
		state, err := r.decode(ctx, r.store.db, &rows[i], false)
		if err != nil {
			return nil, err
		}
		states = append(states, *state)
	}
	return states, nil
}

// Update invokes one bounded local callback under the cross-process writer lock.
// Callback failure or invalid scope rolls back the complete snapshot.
func (r *TelegramBusinessRepository) Update(ctx context.Context, key business.Key, fn func(*business.State) error) error {
	return r.update(ctx, key, fn, "")
}

func (r *TelegramBusinessRepository) update(ctx context.Context, key business.Key, fn func(*business.State) error, cleanupOwner string) error {
	if !validBusinessKey(key) || fn == nil {
		return business.ErrDenied
	}
	return r.store.runWriteTransaction(ctx, func(ctx context.Context, db bun.IDB) error {
		prior, err := readBusinessRow(ctx, db, key)
		if err != nil {
			return err
		}
		if cleanupOwner != "" && (prior == nil || prior.OwnerID != cleanupOwner) {
			return business.ErrDenied
		}
		state := &business.State{Connection: business.Connection{RemoteConnection: business.RemoteConnection{Key: key}}}
		if prior != nil {
			state, err = r.decodeForScope(ctx, db, prior, true, cleanupOwner)
			if errors.Is(err, business.ErrStale) && cleanupOwner == "" {
				state, err = r.decode(ctx, db, prior, false)
			}
			if err != nil {
				return err
			}
		}
		oldOwner, oldTelegramOwner, oldEpoch := state.Connection.OwnerID, state.Connection.OwnerTelegramID, state.Connection.Epoch
		beforeJSON, err := json.Marshal(state)
		if err != nil {
			return err
		}
		var before business.State
		if decodeErr := json.Unmarshal(beforeJSON, &before); decodeErr != nil {
			return decodeErr
		}
		if callbackErr := fn(state); callbackErr != nil {
			return callbackErr
		}
		if state.Connection.Key != key || (oldOwner != "" && oldOwner != state.Connection.OwnerID) || (oldTelegramOwner > 0 && oldTelegramOwner != state.Connection.OwnerTelegramID) {
			return business.ErrDenied
		}
		if state.Connection.Epoch < oldEpoch {
			return business.ErrStale
		}
		if cleanupOwner == "" {
			ownerEpoch, ownerErr := businessOwnerEpoch(ctx, db, state.Connection)
			if ownerErr != nil {
				return ownerErr
			}
			if oldOwner == "" {
				state.Connection.OwnerSecurityEpoch = ownerEpoch
			} else if state.Connection.OwnerSecurityEpoch != ownerEpoch {
				return business.ErrStale
			}
		}
		metadata, err := businessMetadata(state)
		if err != nil {
			return err
		}
		for id, peer := range state.Peers {
			if id <= 0 || peer.ID != id {
				return business.ErrDenied
			}
			checkedPeer := *peer
			if !state.Connection.Policy.Consent {
				checkedPeer.Policy.Consent = false
			}
			if !state.Connection.Policy.MediaConsent {
				checkedPeer.Policy.MediaConsent = false
			}
			if unconsentedBusinessContent(&checkedPeer, before.Peers[id]) {
				return business.ErrDenied
			}
			for draftID, draft := range peer.Drafts {
				if draft == nil || draft.ID != draftID || draft.Scope != (business.Scope{Key: key, OwnerID: state.Connection.OwnerID, PeerID: id}) {
					return business.ErrDenied
				}
			}
			for actionID, action := range peer.Actions {
				if action == nil || action.ID != actionID || action.Scope != (business.Scope{Key: key, OwnerID: state.Connection.OwnerID, PeerID: id}) {
					return business.ErrDenied
				}
			}
		}
		for id, action := range state.AccountActions {
			if action == nil || action.ID != id || action.Scope != (business.Scope{Key: key, OwnerID: state.Connection.OwnerID}) {
				return business.ErrDenied
			}
		}
		metadataJSON, err := json.Marshal(metadata)
		if err != nil {
			return business.ErrUnavailable
		}
		fullJSON, err := json.Marshal(state)
		if err != nil {
			return business.ErrUnavailable
		}
		row := &businessStateRow{InstanceID: key.InstanceID, BotID: key.BotID, ConnectionID: key.ConnectionID, OwnerID: state.Connection.OwnerID, Revision: 1, Metadata: string(metadataJSON), UpdatedAt: time.Now().UTC()}
		if prior != nil {
			if prior.Revision == math.MaxInt64 {
				return business.ErrQuota
			}
			row.Revision = prior.Revision + 1
		}
		if !bytes.Equal(fullJSON, metadataJSON) {
			if state.Connection.OwnerID == "" {
				return business.ErrDenied
			}
			row.Payload, err = r.seal(businessBinding(row), fullJSON)
			if err != nil {
				return err
			}
		}
		_, err = db.NewInsert().Model(row).ModelTableExpr("telegram_business_connections").On("CONFLICT (instance_id,bot_id,connection_id) DO UPDATE").Set("owner_id=EXCLUDED.owner_id, revision=EXCLUDED.revision, metadata=EXCLUDED.metadata, payload=EXCLUDED.payload, updated_at=EXCLUDED.updated_at").Exec(ctx)
		return err
	})
}

var _ business.Repository = (*TelegramBusinessRepository)(nil)

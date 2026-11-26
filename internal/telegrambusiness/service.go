package telegrambusiness

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/iulita-ai/iulita/internal/security"
)

// Service owns isolated connected-account policy; writes are opt-in twice:
// explicit owner policy/approval AND the server's default-off live interlock.
type Service struct {
	repo      Repository
	api       API
	generator Generator
	options   Options
	mu        sync.Mutex
	cancels   map[Key]map[string]workCancellation
}

type workCancellation struct {
	peer   int64
	cancel context.CancelFunc
}

// New does not poll Telegram, register accounts or enable any external action.
func New(repo Repository, api API, generator Generator, options Options) *Service {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.RuntimeGeneration == "" {
		options.RuntimeGeneration = newID()
	}
	return &Service{repo: repo, api: api, generator: generator, options: options, cancels: make(map[Key]map[string]workCancellation)}
}

func newID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}
func (s *Service) now() time.Time { return s.options.Now().UTC() }
func initState(st *State) {
	if st.Peers == nil {
		st.Peers = map[int64]*Peer{}
	}
	if st.AccountActions == nil {
		st.AccountActions = map[string]*Action{}
	}
	if st.Seen == nil {
		st.Seen = map[string]time.Time{}
	}
}
func initPeer(st *State, id int64) *Peer {
	p := st.Peers[id]
	if p == nil {
		p = &Peer{ID: id, Policy: PeerPolicy{Mode: ModeOff}, Drafts: map[string]*Draft{}, Actions: map[string]*Action{}}
		st.Peers[id] = p
	}
	if p.Drafts == nil {
		p.Drafts = map[string]*Draft{}
	}
	if p.Actions == nil {
		p.Actions = map[string]*Action{}
	}
	return p
}
func audit(st *State, now time.Time, kind, id, outcome string) {
	st.Audit = append(st.Audit, Audit{now, kind, id, outcome})
	if len(st.Audit) > 1000 {
		st.Audit = st.Audit[len(st.Audit)-1000:]
	}
}
func invalidatePeer(p *Peer) {
	p.Revision++
	for dIndex := range p.Drafts {
		d := p.Drafts[dIndex]
		if d != nil {
			d.State = "cancelled"
		}
	}
	for aIndex := range p.Actions {
		a := p.Actions[aIndex]
		if a != nil && a.State != "sent" && a.State != "dispatching" && a.State != "unknown" {
			a.State = "cancelled"
		}
	}
}
func invalidate(st *State) {
	st.Connection.Epoch++
	for pIndex := range st.Peers {
		p := st.Peers[pIndex]
		if p != nil {
			invalidatePeer(p)
		}
	}
	for aIndex := range st.AccountActions {
		a := st.AccountActions[aIndex]
		if a != nil && a.State != "sent" && a.State != "dispatching" && a.State != "unknown" {
			a.State = "cancelled"
		}
	}
}

func (s *Service) cancelKey(key Key) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cancel := range s.cancels[key] {
		cancel.cancel()
	}
}
func (s *Service) cancelScope(scope Scope) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range s.cancels[scope.Key] {
		if scope.PeerID == 0 || w.peer == scope.PeerID {
			w.cancel()
		}
	}
}
func (s *Service) work(ctx context.Context, key Key, peer int64) (workCtx context.Context, finish func()) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	id := newID()
	s.mu.Lock()
	if s.cancels[key] == nil {
		s.cancels[key] = map[string]workCancellation{}
	}
	s.cancels[key][id] = workCancellation{peer, cancel}
	s.mu.Unlock()
	return ctx, func() {
		cancel()
		s.mu.Lock()
		delete(s.cancels[key], id)
		if len(s.cancels[key]) == 0 {
			delete(s.cancels, key)
		}
		s.mu.Unlock()
	}
}

func (s *Service) owner(st *State, scope Scope) error {
	if scope.validate() != nil || st == nil || st.Connection.Key != scope.Key || st.Connection.OwnerID != scope.OwnerID || scope.OwnerID == "" {
		return ErrDenied
	}
	return nil
}
func (s *Service) available(st *State, scope Scope) error {
	if checkErr := s.owner(st, scope); checkErr != nil {
		return checkErr
	}
	c := st.Connection
	if !c.Enabled || c.Paused || c.Quarantined || c.NeedsRefresh || c.VerifiedAt.IsZero() || c.RuntimeGeneration != s.options.RuntimeGeneration || s.options.RuntimeGeneration == "" || s.options.RestoreGeneration != "" && c.RestoreGeneration != s.options.RestoreGeneration {
		return ErrUnavailable
	}
	return nil
}

// List returns owner-bound metadata, excluding all message/draft/action bodies.
func (s *Service) List(ctx context.Context, owner string) ([]State, error) {
	if s.repo == nil || strings.TrimSpace(owner) == "" {
		return nil, ErrDenied
	}
	states, e := s.repo.List(ctx, owner)
	if e != nil {
		return nil, e
	}
	out := make([]State, 0, len(states))
	for stIndex := range states {
		st := states[stIndex]
		if st.Connection.OwnerID == owner {
			v := metadata(st)
			v.Connection.NeedsRefresh = v.Connection.NeedsRefresh || v.Connection.RuntimeGeneration != s.options.RuntimeGeneration
			out = append(out, v)
		}
	}
	return out, nil
}
func metadata(st State) State {
	out := State{Connection: st.Connection, Peers: map[int64]*Peer{}}
	for id, p := range st.Peers {
		if p != nil {
			out.Peers[id] = &Peer{ID: id, Policy: p.Policy, Revision: p.Revision, LastIncomingAt: p.LastIncomingAt, LastMediaOmitted: p.LastMediaOmitted, LastProcessingState: p.LastProcessingState, LastProcessingReason: p.LastProcessingReason, LastProcessedAt: p.LastProcessedAt}
			if p.Summary != nil {
				out.Peers[id].Summary = &Summary{CreatedAt: p.Summary.CreatedAt, BeforeBytes: p.Summary.BeforeBytes, AfterBytes: p.Summary.AfterBytes}
			}
		}
	}
	return out
}

// GetState exposes only metadata after verifying the authenticated owner.
func (s *Service) GetState(ctx context.Context, owner string, key Key) (*State, error) {
	states, e := s.List(ctx, owner)
	if e != nil {
		return nil, e
	}
	for stIndex := range states {
		st := states[stIndex]
		if st.Connection.Key == key {
			return &st, nil
		}
	}
	return nil, ErrUnavailable
}

// Peers enumerates only observed connection-local peer metadata.
func (s *Service) Peers(ctx context.Context, owner string, key Key) ([]Peer, error) {
	st, e := s.GetState(ctx, owner, key)
	if e != nil {
		return nil, e
	}
	out := make([]Peer, 0, len(st.Peers))
	for pIndex := range st.Peers {
		p := st.Peers[pIndex]
		if p != nil {
			out = append(out, *p)
		}
	}
	return out, nil
}

// Messages reads retained source content only for an enabled, consented scope.
func (s *Service) Messages(ctx context.Context, scope Scope) ([]Message, error) {
	st, e := s.read(ctx, scope.Key)
	if e != nil {
		return nil, e
	}
	prune(st, s.now())
	if checkErr := s.available(st, scope); checkErr != nil {
		return nil, checkErr
	}
	p := st.Peers[scope.PeerID]
	if p == nil || !st.Connection.Policy.Enabled || !st.Connection.Policy.Consent || !p.Policy.Enabled || !p.Policy.Consent {
		return nil, ErrDenied
	}
	var out []Message
	for mIndex := range p.Messages {
		m := p.Messages[mIndex]
		if !m.Deleted && !m.Date.Before(s.now().Add(-7*24*time.Hour)) {
			m.Media = append([]Media(nil), m.Media...)
			out = append(out, m)
		}
	}
	return out, nil
}

// Configure changes policy only in the already verified owner's control plane.
func (s *Service) Configure(ctx context.Context, in ConfigureInput) error {
	if in.Scope.validate() != nil {
		return ErrDenied
	}
	if in.Policy.Mode != "" && in.Policy.Mode != ModeOff && in.Policy.Mode != ModeDraft && in.Policy.Mode != ModeConfirm && in.Policy.Mode != ModeAuto {
		return ErrDenied
	}
	if in.Policy.Enabled && !in.Policy.Consent || len(in.Policy.Provider) > 128 {
		return ErrDenied
	}
	if len(in.Policy.MediaProvider) > 256 || in.Policy.MediaConsent && strings.TrimSpace(in.Policy.MediaProvider) == "" || security.RedactSensitiveText(in.Policy.MediaProvider) != in.Policy.MediaProvider {
		return ErrDenied
	}
	if security.RedactSensitiveText(in.Policy.Provider) != in.Policy.Provider || len(in.Policy.AllowedMethods) > len(methodSpecs) {
		return ErrDenied
	}
	if checkErr := validateAutoRules(in.Policy.AutoRules); checkErr != nil {
		return checkErr
	}
	seenMethods := map[Method]bool{}
	for mIndex := range in.Policy.AllowedMethods {
		m := in.Policy.AllowedMethods[mIndex]
		if _, ok := methodSpecs[m]; !ok {
			return ErrUnsupported
		}
		if seenMethods[m] {
			return ErrDenied
		}
		seenMethods[m] = true
	}
	e := s.update(ctx, in.Scope.Key, func(st *State) error {
		initState(st)
		if checkErr := s.owner(st, in.Scope); checkErr != nil {
			return checkErr
		}
		if st.Connection.Quarantined {
			return ErrUnavailable
		}
		var p *PeerPolicy
		if in.Scope.PeerID == 0 {
			p = &st.Connection.Policy
		} else {
			p = &initPeer(st, in.Scope.PeerID).Policy
		}
		if in.Scope.PeerID == 0 {
			if st.Connection.Epoch != in.ExpectedRevision {
				return ErrStale
			}
		} else if st.Peers[in.Scope.PeerID].Revision != in.ExpectedRevision {
			return ErrStale
		}
		mediaChanged := p.MediaConsent != in.Policy.MediaConsent || p.MediaProvider != in.Policy.MediaProvider
		if mediaChanged {
			if in.Scope.PeerID == 0 {
				for peerIndex := range st.Peers {
					peer := st.Peers[peerIndex]
					eraseMediaDerived(peer)
				}
			} else {
				eraseMediaDerived(st.Peers[in.Scope.PeerID])
			}
		}
		v := in.Policy
		v.Revision = p.Revision + 1
		if v.Mode == "" {
			v.Mode = ModeOff
		}
		*p = v
		if in.Scope.PeerID == 0 {
			invalidate(st)
		} else {
			invalidatePeer(st.Peers[in.Scope.PeerID])
		}
		audit(st, s.now(), "policy", "", "updated")
		return nil
	})
	if e == nil {
		s.cancelScope(in.Scope)
	}
	return e
}

// Pause closes admission and invalidates all earlier approvals in the scope.
func (s *Service) Pause(ctx context.Context, scope Scope, expectedRevision uint64, paused bool) error {
	e := s.update(ctx, scope.Key, func(st *State) error {
		initState(st)
		if checkErr := s.owner(st, scope); checkErr != nil {
			return checkErr
		}
		if scope.PeerID == 0 {
			if st.Connection.Epoch != expectedRevision {
				return ErrStale
			}
			st.Connection.Paused = paused
		} else {
			p := st.Peers[scope.PeerID]
			if p == nil || p.Revision != expectedRevision {
				return ErrStale
			}
			p.Policy.Enabled = !paused
		}
		if scope.PeerID == 0 {
			invalidate(st)
		} else {
			invalidatePeer(st.Peers[scope.PeerID])
		}
		audit(st, s.now(), "pause", "", "updated")
		return nil
	})
	if e == nil {
		s.cancelScope(scope)
	}
	return e
}

// Reconcile refreshes known connection metadata and cannot bind a new owner.
func (s *Service) Reconcile(ctx context.Context, key Key) (resultErr error) {
	started := time.Now()
	defer func() { s.observeResult("reconcile", "", started, resultErr) }()
	if s.api == nil || key.Validate() != nil {
		return ErrUnavailable
	}
	var before Connection
	if checkErr := s.update(ctx, key, func(st *State) error { before = st.Connection; return nil }); checkErr != nil {
		return checkErr
	}
	work, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	remote, e := s.api.GetConnection(work, key)
	if e != nil {
		return e
	}
	if remote.Key != key || remote.OwnerTelegramID <= 0 || remote.OwnerChatID <= 0 {
		return ErrDenied
	}
	changedAuthority := false
	e = s.update(ctx, key, func(st *State) error {
		initState(st)
		if st.Connection.Epoch != before.Epoch {
			return ErrStale
		}
		if st.Connection.OwnerTelegramID != 0 && st.Connection.OwnerTelegramID != remote.OwnerTelegramID {
			return ErrDenied
		}
		runtimeChanged := st.Connection.RuntimeGeneration != s.options.RuntimeGeneration
		changed := runtimeChanged || st.Connection.Enabled != remote.Enabled || !reflect.DeepEqual(st.Connection.Rights, remote.Rights)
		st.Connection.RemoteConnection = remote
		st.Connection.VerifiedAt = s.now()
		st.Connection.NeedsRefresh = false
		if runtimeChanged {
			restartFence(st)
			st.Connection.RuntimeGeneration = s.options.RuntimeGeneration
		}
		if s.options.RestoreGeneration != "" && st.Connection.RestoreGeneration != s.options.RestoreGeneration {
			st.Connection.Quarantined = true
		}
		if changed {
			invalidate(st)
			changedAuthority = true
		}
		return nil
	})
	if e == nil && changedAuthority {
		s.cancelKey(key)
	}
	return e
}

// Handle accepts trusted typed events, never SDK Messages. Deferred source
// intake cannot automatically generate before AdmitProcessCandidate.
func (s *Service) Handle(ctx context.Context, event Event) (resultErr error) {
	started := time.Now()
	deduplicated, quarantined := false, false
	defer func() {
		s.observeResult("ingress", event.Kind, started, resultErr)
		if resultErr == nil && deduplicated {
			s.observe(Observation{Operation: "ingress", Kind: event.Kind, Outcome: "dedup"})
		}
		if resultErr == nil && quarantined {
			s.observe(Observation{Operation: "ingress", Kind: event.Kind, Outcome: "quarantined"})
		}
	}()
	if s.repo == nil || event.Key.Validate() != nil || event.UpdateID < 0 || event.Generation == "" || len(event.Generation) > 128 {
		return ErrDenied
	}
	if event.Kind == "connection" {
		if event.Connection == nil || event.Connection.Key != event.Key || event.Connection.OwnerTelegramID <= 0 || event.Connection.OwnerChatID <= 0 {
			return ErrDenied
		}
		var before Connection
		var refresh bool
		if e := s.update(ctx, event.Key, func(st *State) error {
			before = st.Connection
			refresh = before.DeliveryGeneration != "" && before.DeliveryGeneration != event.Generation || before.RuntimeGeneration != "" && before.RuntimeGeneration != s.options.RuntimeGeneration
			return nil
		}); e != nil {
			return e
		}
		remote := *event.Connection
		if refresh && remote.Enabled {
			if s.api == nil {
				return ErrUnavailable
			}
			var e error
			remote, e = s.api.GetConnection(ctx, event.Key)
			if e != nil {
				return e
			}
			if remote.Key != event.Key {
				return ErrDenied
			}
		}
		changedAuthority := false
		e := s.update(ctx, event.Key, func(st *State) error {
			initState(st)
			c := &st.Connection
			if c.Epoch != before.Epoch {
				return ErrStale
			}
			if c.OwnerID != "" && event.OwnerID != "" && c.OwnerID != event.OwnerID {
				return ErrDenied
			}
			if c.OwnerTelegramID != 0 && c.OwnerTelegramID != remote.OwnerTelegramID {
				return ErrDenied
			}
			if c.DeliveryGeneration == event.Generation && event.UpdateID <= c.ControlUpdateID && c.RuntimeGeneration == s.options.RuntimeGeneration {
				deduplicated = true
				return nil
			}
			runtimeChanged := c.RuntimeGeneration != s.options.RuntimeGeneration
			changed := runtimeChanged || !reflect.DeepEqual(c.Rights, remote.Rights) || c.Enabled != remote.Enabled || refresh
			c.RemoteConnection = remote
			c.OwnerID = event.OwnerID
			if c.OwnerID == "" {
				c.OwnerID = before.OwnerID
			}
			c.Quarantined = c.OwnerID == "" || before.Quarantined && before.OwnerID != ""
			if s.options.RestoreGeneration != "" && c.RestoreGeneration != "" && c.RestoreGeneration != s.options.RestoreGeneration {
				c.Quarantined = true
			}
			if c.RestoreGeneration == "" {
				c.RestoreGeneration = s.options.RestoreGeneration
			}
			c.DeliveryGeneration = event.Generation
			c.ControlUpdateID = event.UpdateID
			c.VerifiedAt = s.now()
			c.NeedsRefresh = false
			if runtimeChanged {
				restartFence(st)
				c.RuntimeGeneration = s.options.RuntimeGeneration
			}
			if changed {
				invalidate(st)
				changedAuthority = true
			}
			quarantined = c.Quarantined
			audit(st, s.now(), "connection", "", "observed")
			return nil
		})
		if e == nil && changedAuthority {
			s.cancelKey(event.Key)
		}
		return e
	}
	if event.PeerID <= 0 {
		return ErrDenied
	}
	if event.Kind == "delete" {
		if store, ok := s.repo.(DeletionRepository); ok {
			e := store.ApplyTelegramBusinessDeletion(ctx, event.Key, event.OwnerID, event.PeerID, event.DeletedIDs, s.now())
			if e == nil {
				s.cancelScope(Scope{Key: event.Key, OwnerID: event.OwnerID, PeerID: event.PeerID})
			}
			return e
		}
	}
	if event.Connection != nil {
		// The transport attaches only freshly fetched metadata, not a message-
		// supplied owner claim. Bootstrap still goes through the control path.
		if checkErr := s.Handle(ctx, Event{Key: event.Key, OwnerID: event.OwnerID, Generation: event.Generation, UpdateID: event.UpdateID, Kind: "connection", Connection: event.Connection}); checkErr != nil {
			return checkErr
		}
	}
	return s.update(ctx, event.Key, func(st *State) error {
		initState(st)
		scope := Scope{event.Key, event.OwnerID, event.PeerID}
		admission := s.available
		if event.Kind == "delete" {
			admission = s.owner
		}
		if checkErr := admission(st, scope); checkErr != nil {
			return checkErr
		}
		if st.Connection.DeliveryGeneration != "" && st.Connection.DeliveryGeneration != event.Generation {
			return ErrStale
		}
		now := s.now()
		for k, t := range st.Seen {
			if t.Before(now.Add(-24 * time.Hour)) {
				delete(st.Seen, k)
			}
		}
		seen := fmt.Sprintf("%s:%d:%s", event.Generation, event.UpdateID, event.Kind)
		if _, ok := st.Seen[seen]; ok {
			deduplicated = true
			return nil
		}
		if len(st.Seen) >= 1000 {
			return ErrQuota
		}
		st.Seen[seen] = now
		if _, ok := st.Peers[event.PeerID]; !ok && len(st.Peers) >= 100 {
			return ErrQuota
		}
		p := initPeer(st, event.PeerID)
		if event.Kind == "delete" {
			if len(event.DeletedIDs) == 0 || len(event.DeletedIDs) > 100 {
				return ErrDenied
			}
			ids := map[int]bool{}
			for idIndex := range event.DeletedIDs {
				id := event.DeletedIDs[idIndex]
				if id <= 0 {
					return ErrDenied
				}
				ids[id] = true
			}
			for i := range p.Messages {
				if ids[p.Messages[i].ID] {
					p.Messages[i].Deleted = true
					p.Messages[i].Text = ""
					p.Messages[i].Media = nil
					p.Messages[i].MediaGroupID = ""
				}
			}
			for id := range ids {
				found := false
				for mIndex := range p.Messages {
					m := p.Messages[mIndex]
					if m.ID == id {
						found = true
					}
				}
				if !found {
					p.Messages = append(p.Messages, Message{ID: id, Deleted: true, Date: now})
				}
			}
			if len(p.Messages) > 1000 {
				// A finite timestamp barrier replaces an overflowing tombstone set.
				// It cannot reactivate pre-barrier messages after owner re-enables.
				p.ReplayAfter = now
				p.Messages = nil
				p.Policy.Enabled = false
			}
			invalidatePeer(p)
			clearDerived(p)
			audit(st, now, "delete", "", "applied")
			return nil
		}
		if event.Kind != "message" && event.Kind != "edit" {
			return ErrUnsupported
		}
		if event.Message == nil || event.Message.ID <= 0 {
			return ErrDenied
		}
		m := *event.Message
		if m.Date.IsZero() || m.Date.After(now) || m.Date.Before(now.Add(-7*24*time.Hour)) || !p.ReplayAfter.IsZero() && !m.Date.After(p.ReplayAfter) {
			return ErrStale
		}
		switch {
		case m.Direction == "bot_outgoing":
		// Only the verified current business bot produces owned outgoing proof.
		case m.SenderID == st.Connection.OwnerTelegramID:
			m.Direction = "owner_outgoing"
		case m.SenderIsBot:
			m.Direction = "bot_incoming"
		default:
			if m.SenderID != event.PeerID {
				audit(st, now, "message", "", "unknown_sender")
				return nil
			}
			m.Direction = "incoming"
		}
		if m.Direction == "owner_outgoing" && p.Policy.AutoReply {
			p.Policy.AutoReply = false
			if p.Policy.Mode == ModeAuto {
				p.Policy.Mode = ModeDraft
			}
			p.Policy.Revision++
		}
		knownMessage := false
		for oldIndex := range p.Messages {
			old := p.Messages[oldIndex]
			if old.ID == m.ID {
				knownMessage = true
				break
			}
		}
		if !knownMessage && event.Kind == "message" && m.Direction == "incoming" && m.Date.After(p.LastIncomingAt) {
			p.LastIncomingAt = m.Date
		}
		if !st.Connection.Policy.Enabled || !st.Connection.Policy.Consent || !p.Policy.Enabled || !p.Policy.Consent {
			audit(st, now, "message", "", "metadata_only")
			return nil
		}
		if len(m.Text) > 65536 || !utf8.ValidString(m.Text) || len(m.Media) > 8 {
			return ErrQuota
		}
		m.Text = security.RedactSensitiveText(m.Text)
		for aIndex := range m.Media {
			a := m.Media[aIndex]
			if a.Bytes < 0 || a.Bytes > 10<<20 || a.DurationSeconds < 0 || a.DurationSeconds > 120 || len(a.FileID) > 512 {
				return ErrQuota
			}
		}
		for i := range p.Messages {
			old := p.Messages[i]
			if old.ID != m.ID {
				continue
			}
			if old.Deleted || m.Revision <= old.Revision {
				deduplicated = true
				return nil
			}
			p.Messages[i] = m
			p.Summary = nil
			invalidatePeer(p)
			if !event.DeferProcessing {
				p.ProcessRevision = p.Revision
			}
			return nil
		}
		if event.Kind == "edit" {
			return nil
		}
		p.Messages = append(p.Messages, m)
		live := 0
		for i := len(p.Messages) - 1; i >= 0; i-- {
			if !p.Messages[i].Deleted {
				live++
				if live > 100 {
					p.Messages[i].Text = ""
					p.Messages[i].Media = nil
					p.Messages[i].MediaGroupID = ""
					p.Messages[i].Deleted = true
				}
			}
		}
		if len(p.Messages) > 1000 {
			p.ReplayAfter = now
			p.Messages = nil
			p.Policy.Enabled = false
		}
		invalidatePeer(p)
		if !event.DeferProcessing {
			p.ProcessRevision = p.Revision
		}
		audit(st, now, "message", "", "retained")
		return nil
	})
}

// Draft generates from this peer's retained raw messages with no private memory.
func (s *Service) Draft(ctx context.Context, scope Scope) (*Draft, error) {
	return s.draft(ctx, scope, nil)
}
func (s *Service) draft(ctx context.Context, scope Scope, expected *uint64) (*Draft, error) {
	if s.generator == nil || s.options.Budget == nil || scope.PeerID <= 0 {
		return nil, ErrUnavailable
	}
	if checkErr := s.Reconcile(ctx, scope.Key); checkErr != nil {
		return nil, checkErr
	}
	st, p, messages, e := s.scopedContext(ctx, scope)
	if e != nil {
		return nil, e
	}
	if checkErr := expectedScope(st, scope, expected); checkErr != nil {
		return nil, checkErr
	}
	if len(messages) == 0 {
		return nil, ErrUnavailable
	}
	if contextBytes(messages) > 15000 {
		if _, e = s.Compact(ctx, scope); e != nil {
			return nil, e
		}
		st, p, messages, e = s.scopedContext(ctx, scope)
		if e != nil {
			return nil, e
		}
	}
	if contextBytes(messages) > 16000 {
		return nil, ErrQuota
	}
	epoch, revision, ownerRevision := st.Connection.Epoch, p.Revision, p.OwnerDraftRevision
	text, e := s.generateIsolated(ctx, scope, epoch, revision, p.Policy, messages, "draft")
	if e != nil {
		return nil, e
	}
	text = security.RedactSensitiveText(text)
	if strings.TrimSpace(text) == "" || len(text) > 4000 {
		return nil, ErrQuota
	}
	d := &Draft{ID: newID(), Scope: scope, Epoch: epoch, ContextRevision: revision, Revision: 1, Text: text, CreatedAt: s.now(), ExpiresAt: s.now().Add(24 * time.Hour), State: "draft"}
	if d.ID == "" {
		return nil, ErrUnavailable
	}
	e = s.update(ctx, scope.Key, func(current *State) error {
		if checkErr := s.available(current, scope); checkErr != nil {
			return checkErr
		}
		peer := current.Peers[scope.PeerID]
		if current.Connection.Epoch != epoch || peer == nil || peer.Revision != revision || peer.OwnerDraftRevision != ownerRevision || !peer.Policy.Enabled || !peer.Policy.Consent {
			return ErrStale
		}
		for id, old := range peer.Drafts {
			if old.State != "sent" {
				delete(peer.Drafts, id)
			}
		}
		peer.Drafts[d.ID] = d
		audit(current, s.now(), "draft", d.ID, "ready")
		return nil
	})
	if e != nil {
		return nil, e
	}
	return d, nil
}

// Edit invalidates approval and returns the new exact owner-edited draft.
func (s *Service) Edit(ctx context.Context, scope Scope, id string, revision uint64, text string) (*Draft, error) {
	if text == "" || len(text) > 4000 || !utf8.ValidString(text) || security.RedactSensitiveText(text) != text {
		return nil, ErrDenied
	}
	var out Draft
	e := s.update(ctx, scope.Key, func(st *State) error {
		if checkErr := s.available(st, scope); checkErr != nil {
			return checkErr
		}
		p := st.Peers[scope.PeerID]
		if p == nil {
			return ErrUnavailable
		}
		d := p.Drafts[id]
		if d == nil || d.Revision != revision || d.Epoch != st.Connection.Epoch || d.ContextRevision != p.Revision || !d.ExpiresAt.After(s.now()) {
			return ErrStale
		}
		d.Revision++
		d.Text = text
		d.ActionID = ""
		p.OwnerDraftRevision++
		d.State = "draft"
		for aIndex := range p.Actions {
			a := p.Actions[aIndex]
			if a.State == "prepared" || a.State == "approved" {
				a.State = "cancelled"
			}
		}
		out = *d
		return nil
	})
	return &out, e
}

// Purge erases retained content, preserves replay tombstones and cancels work.
func (s *Service) Purge(ctx context.Context, scope Scope) error { return s.purge(ctx, scope, nil) }
func (s *Service) purge(ctx context.Context, scope Scope, expected *uint64) error {
	e := s.update(ctx, scope.Key, func(st *State) error {
		if checkErr := s.owner(st, scope); checkErr != nil {
			return checkErr
		}
		if checkErr := expectedScope(st, scope, expected); checkErr != nil {
			return checkErr
		}
		erase := func(p *Peer) {
			p.Summary = nil
			p.AutoClaim = ""
			p.OwnerDraftRevision++
			p.Policy = PeerPolicy{Mode: ModeOff, Revision: p.Policy.Revision + 1}

			for i := range p.Messages {
				p.Messages[i].Text = ""
				p.Messages[i].Media = nil
				p.Messages[i].MediaGroupID = ""
				p.Messages[i].Deleted = true
			}
			p.Drafts = map[string]*Draft{}
			for aIndex := range p.Actions {
				a := p.Actions[aIndex]
				a.Params = nil
				a.Receipt = nil
				if a.State != "dispatching" && a.State != "sent" && a.State != "unknown" {
					a.State = "cancelled"
				}
			}
			p.Policy.Enabled = false
		}
		if scope.PeerID == 0 {
			st.Connection.Policy = PeerPolicy{Mode: ModeOff, Revision: st.Connection.Policy.Revision + 1}
			for pIndex := range st.Peers {
				p := st.Peers[pIndex]
				erase(p)
			}
			for aIndex := range st.AccountActions {
				a := st.AccountActions[aIndex]
				a.Params = nil
				a.Receipt = nil
				if a.State != "dispatching" && a.State != "sent" && a.State != "unknown" {
					a.State = "cancelled"
				}
			}
		} else {
			p := st.Peers[scope.PeerID]
			if p == nil {
				return ErrUnavailable
			}
			erase(p)
		}
		if scope.PeerID == 0 {
			invalidate(st)
		} else {
			invalidatePeer(st.Peers[scope.PeerID])
		}
		audit(st, s.now(), "purge", "", "applied")
		return nil
	})
	if e == nil {
		s.cancelScope(scope)
	}
	return e
}

// Export returns a bounded owner-authorized snapshot, never a public URL.
func (s *Service) Export(ctx context.Context, scope Scope) ([]byte, error) {
	st, p, _, e := s.scopedContext(ctx, scope)
	if e != nil {
		return nil, e
	}
	epoch, revision := st.Connection.Epoch, p.Revision
	var messages []Message
	for mIndex := range p.Messages {
		m := p.Messages[mIndex]
		if !m.Deleted {
			messages = append(messages, m)
		}
	}
	raw, e := json.Marshal(struct {
		Scope    Scope     `json:"scope"`
		Messages []Message `json:"messages"`
	}{scope, messages})
	if e != nil {
		return nil, e
	}
	if len(raw) > 10<<20 {
		return nil, ErrQuota
	}
	current, e := s.read(ctx, scope.Key)
	if e != nil || s.available(current, scope) != nil || current.Connection.Epoch != epoch || current.Peers[scope.PeerID] == nil || current.Peers[scope.PeerID].Revision != revision {
		return nil, ErrUnavailable
	}
	return raw, nil
}

// APIError marks only definitely rejected API requests as safely terminal.
// Every other transport error is ambiguous and is never automatically retried.
type APIError struct {
	Cause    error
	Rejected bool
}

func (e *APIError) Error() string { return "business API request failed" }
func (e *APIError) Unwrap() error { return e.Cause }
func rejected(err error) bool     { var e *APIError; return errors.As(err, &e) && e.Rejected }

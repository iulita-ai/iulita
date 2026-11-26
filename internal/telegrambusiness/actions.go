package telegrambusiness

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/iulita-ai/iulita/internal/security"
)

// LiveWritesEnabled exposes the server interlock for the transport adapter.
// No owner settings or received Telegram right can change this option.
func (s *Service) LiveWritesEnabled() bool { return s.options.LiveWrites }

func actionMap(st *State, scope Scope) map[string]*Action {
	if scope.PeerID == 0 {
		return st.AccountActions
	}
	if p := st.Peers[scope.PeerID]; p != nil {
		return p.Actions
	}
	return nil
}
func policyFor(st *State, scope Scope) (PeerPolicy, uint64, error) {
	if !st.Connection.Policy.Enabled || !st.Connection.Policy.Consent {
		return PeerPolicy{}, 0, ErrDenied
	}
	if scope.PeerID == 0 {
		return st.Connection.Policy, 0, nil
	}
	p := st.Peers[scope.PeerID]
	if p == nil || !p.Policy.Enabled || !p.Policy.Consent {
		return PeerPolicy{}, 0, ErrDenied
	}
	return p.Policy, p.Revision, nil
}
func allowedMethod(p PeerPolicy, m Method) bool {
	for aIndex := range p.AllowedMethods {
		a := p.AllowedMethods[aIndex]
		if a == m {
			return true
		}
	}
	return false
}
func (s *Service) actionGuard(st *State, a *Action) error {
	if a == nil {
		return ErrUnavailable
	}
	if checkErr := s.available(st, a.Scope); checkErr != nil {
		return checkErr
	}
	p, rev, e := policyFor(st, a.Scope)
	if e != nil {
		return e
	}
	if st.Connection.Epoch != a.Epoch || rev != a.ContextRevision || p.Revision != a.PolicyRevision || !a.ExpiresAt.After(s.now()) || a.Digest != digestAction(*a) {
		return ErrStale
	}
	if !allowedMethod(p, a.Method) || !st.Connection.Rights.Allows(a.Method, a.Params) {
		return ErrDenied
	}
	if st.Connection.Policy.Mode != ModeConfirm && st.Connection.Policy.Mode != ModeAuto {
		return ErrDenied
	}
	if a.Scope.PeerID > 0 && p.Mode != ModeConfirm && p.Mode != ModeAuto {
		return ErrDenied
	}
	if a.Automatic {
		if a.Method != SendText {
			return ErrDenied
		}
		if checkErr := s.autoAllowed(st, a.Scope, a.ID); checkErr != nil {
			return checkErr
		}
	}
	if methodSpecs[a.Method].financial {
		limits := s.options.FinancialLimits
		if limits.DailyOperations <= 0 || limits.DailyStars <= 0 {
			return ErrUnavailable
		}
		ops := 0
		spent := int64(0)
		today := s.now().Truncate(24 * time.Hour)
		for oldIndex := range st.AccountActions {
			old := st.AccountActions[oldIndex]
			if old == nil || old.ID == a.ID || !methodSpecs[old.Method].financial || old.State == "cancelled" || old.State == "failed" || old.State == "expired" {
				continue
			}
			at := old.DispatchAt
			if at.IsZero() {
				at = old.CreatedAt
			}
			if at.Before(today) {
				continue
			}
			ops++
			if old.ReservedStars < 0 || old.ReservedStars > 10000 {
				return ErrDenied
			}
			spent += old.ReservedStars
		}
		cost := int64(0)
		if _, ok := a.Params["star_count"]; ok {
			var e error
			cost, e = intParam(a.Params, "star_count", 0, 10000)
			if e != nil {
				return e
			}
		}
		if ops >= limits.DailyOperations || cost > limits.DailyStars-spent {
			return ErrQuota
		}
	}
	if a.Scope.PeerID > 0 {
		peer := st.Peers[a.Scope.PeerID]
		if a.Method == SendText || a.Method == SendPhoto || a.Method == SendDocument || a.Method == SendAudio || a.Method == SendVoice || a.Method == SendVideo || a.Method == EditText || a.Method == ReadMessage {
			if peer.LastIncomingAt.IsZero() || !peer.LastIncomingAt.Add(24*time.Hour-30*time.Second).After(s.now()) {
				return ErrStale
			}
		}
		if a.DraftID != "" {
			d := peer.Drafts[a.DraftID]
			if d == nil || d.Revision != a.DraftRevision || d.Epoch != a.Epoch || d.ContextRevision != a.ContextRevision || d.ActionID != a.ID || d.State == "cancelled" || !d.ExpiresAt.After(s.now()) {
				return ErrStale
			}
		}
	}
	return validateHandles(st, a, s.now())
}

func validateHandles(st *State, a *Action, now time.Time) error {
	if a.Scope.PeerID == 0 {
		if a.Method == TransferGift {
			recipient, e := intParam(a.Params, "new_owner_chat_id", 1, 1<<52)
			if e != nil {
				return e
			}
			peer := st.Peers[recipient]
			if peer == nil || peer.LastIncomingAt.IsZero() || !peer.LastIncomingAt.Add(24*time.Hour-30*time.Second).After(now) {
				return ErrStale
			}
		}
		if a.Method == EditStory || a.Method == DeleteStory {
			id, e := intParam(a.Params, "story_id", 1, 1<<31-1)
			if e != nil {
				return e
			}
			if !knownStory(st, int(id)) {
				return ErrDenied
			}
		}
		return nil
	}
	p := st.Peers[a.Scope.PeerID]
	if p == nil {
		return ErrDenied
	}
	var ids []int
	if a.Method == DeleteSent || a.Method == DeleteAll {
		if json.Unmarshal(a.Params["message_ids"], &ids) != nil {
			return ErrDenied
		}
	}
	if a.Method == EditText || a.Method == ReadMessage {
		n, e := intParam(a.Params, "message_id", 1, 1<<31-1)
		if e != nil {
			return e
		}
		ids = []int{int(n)}
	}
	for idIndex := range ids {
		id := ids[idIndex]
		found := false
		for mIndex := range p.Messages {
			m := p.Messages[mIndex]
			if m.ID == id && !m.Deleted {
				if (a.Method == DeleteSent || a.Method == EditText) && m.Direction != "bot_outgoing" {
					return ErrDenied
				}
				if a.Method == ReadMessage && m.Direction != "incoming" {
					return ErrDenied
				}
				found = true
				break
			}
		}
		if !found {
			return ErrDenied
		}
	}
	return nil
}

func knownStory(st *State, id int) bool {
	for aIndex := range st.AccountActions {
		a := st.AccountActions[aIndex]
		if a != nil && a.State == "sent" && (a.Method == PostStory || a.Method == RepostStory) && a.Receipt != nil && a.Receipt.StoryID == id {
			return true
		}
	}
	return false
}

// CreateDraft stores owner-entered text without invoking a model/provider.
func (s *Service) CreateDraft(ctx context.Context, scope Scope, text string) (*Draft, error) {
	return s.createDraft(ctx, scope, text, nil)
}
func (s *Service) createDraft(ctx context.Context, scope Scope, text string, expected *uint64) (*Draft, error) {
	if strings.TrimSpace(text) == "" || len(text) > 4000 || !utf8.ValidString(text) || scope.PeerID <= 0 {
		return nil, ErrDenied
	}
	id := newID()
	if id == "" {
		return nil, ErrUnavailable
	}
	var out Draft
	e := s.update(ctx, scope.Key, func(st *State) error {
		if checkErr := s.available(st, scope); checkErr != nil {
			return checkErr
		}
		if checkErr := expectedScope(st, scope, expected); checkErr != nil {
			return checkErr
		}
		p, rev, e := policyFor(st, scope)
		if e != nil {
			return e
		}
		_ = p
		peer := st.Peers[scope.PeerID]
		peer.OwnerDraftRevision++
		for k, d := range peer.Drafts {
			if d.State != "sent" {
				delete(peer.Drafts, k)
			}
		}
		out = Draft{ID: id, Scope: scope, Epoch: st.Connection.Epoch, ContextRevision: rev, Revision: 1, Text: text, CreatedAt: s.now(), ExpiresAt: s.now().Add(24 * time.Hour), State: "draft"}
		peer.Drafts[id] = &out
		audit(st, s.now(), "draft", id, "manual")
		return nil
	})
	if e != nil {
		return nil, e
	}
	return &out, nil
}

// Prepare persists a complete exact intent; it never calls a Telegram action.
func (s *Service) Prepare(ctx context.Context, in PrepareInput) (*Action, error) {
	if in.Scope.validate() != nil {
		return nil, ErrDenied
	}
	params, e := normalizeParams(in.Method, in.Params)
	if e != nil {
		return nil, e
	}
	if checkErr := s.verifyAssets(ctx, &Action{Scope: in.Scope, Method: in.Method, Params: params}); checkErr != nil {
		return nil, checkErr
	}
	spec := methodSpecs[in.Method]
	if spec.peer && in.Scope.PeerID == 0 || !spec.peer && in.Scope.PeerID != 0 {
		return nil, ErrDenied
	}
	id := newID()
	if id == "" {
		return nil, ErrUnavailable
	}
	var out Action
	var sourceKey *Key
	var sourceEpoch uint64
	if in.Method == RepostStory {
		from, fromErr := intParam(params, "from_chat_id", 1, 1<<52)
		if fromErr != nil {
			return nil, fromErr
		}
		story, storyErr := intParam(params, "from_story_id", 1, 1<<31-1)
		if storyErr != nil {
			return nil, storyErr
		}
		states, listErr := s.List(ctx, in.Scope.OwnerID)
		if listErr != nil {
			return nil, listErr
		}
		for metaIndex := range states {
			meta := states[metaIndex]
			if meta.Connection.OwnerTelegramID == from {
				source, sourceErr := s.read(ctx, meta.Connection.Key)
				if sourceErr != nil {
					return nil, sourceErr
				}
				if s.available(source, Scope{Key: meta.Connection.Key, OwnerID: in.Scope.OwnerID}) != nil || !source.Connection.Policy.Enabled || !source.Connection.Policy.Consent || !source.Connection.Rights.CanManageStories || !knownStory(source, int(story)) {
					return nil, ErrDenied
				}
				key := meta.Connection.Key
				sourceKey = &key
				sourceEpoch = source.Connection.Epoch
				break
			}
		}
		if sourceKey == nil {
			return nil, ErrDenied
		}
	}
	e = s.update(ctx, in.Scope.Key, func(st *State) error {
		initState(st)
		if checkErr := s.available(st, in.Scope); checkErr != nil {
			return checkErr
		}
		if checkErr := expectedScope(st, in.Scope, in.ExpectedRevision); checkErr != nil {
			return checkErr
		}
		policy, rev, policyErr := policyFor(st, in.Scope)
		if policyErr != nil {
			return policyErr
		}
		if !allowedMethod(policy, in.Method) || !st.Connection.Rights.Allows(in.Method, params) {
			return ErrDenied
		}
		actions := actionMap(st, in.Scope)
		pending := 0
		for aIndex := range actions {
			a := actions[aIndex]
			if a != nil && (a.State == "prepared" || a.State == "approved" || a.State == "dispatching") {
				pending++
			}
		}
		if pending >= 100 || len(actions) >= 5000 {
			return ErrQuota
		}
		out = Action{ID: id, Scope: in.Scope, Epoch: st.Connection.Epoch, ContextRevision: rev, PolicyRevision: policy.Revision, Method: in.Method, Params: params, State: "prepared", RequiredSteps: 1, CreatedAt: s.now(), ExpiresAt: s.now().Add(5 * time.Minute)}
		out.SourceKey = sourceKey
		out.SourceEpoch = sourceEpoch
		if spec.financial {
			out.RequiredSteps = 2
			if _, ok := params["star_count"]; ok {
				reserved, reserveErr := intParam(params, "star_count", 0, 10000)
				if reserveErr != nil {
					return reserveErr
				}
				out.ReservedStars = reserved
			}
		}
		if in.DraftID != "" {
			if in.Method != SendText {
				return ErrDenied
			}
			p := st.Peers[in.Scope.PeerID]
			d := p.Drafts[in.DraftID]
			if d == nil || d.State != "draft" || d.Epoch != out.Epoch || d.ContextRevision != rev || !d.ExpiresAt.After(s.now()) {
				return ErrStale
			}
			var text string
			if json.Unmarshal(params["text"], &text) != nil || text != d.Text {
				return ErrStale
			}
			if d.ActionID != "" {
				old := actions[d.ActionID]
				if old != nil && (old.State == "prepared" || old.State == "approved") {
					return ErrStale
				}
			}
			out.DraftID = d.ID
			out.DraftRevision = d.Revision
			d.ActionID = out.ID
		}
		out.Digest = digestAction(out)
		actions[id] = &out
		if checkErr := s.actionGuard(st, &out); checkErr != nil {
			return checkErr
		}
		audit(st, s.now(), "action", id, "prepared")
		return nil
	})
	if e != nil {
		return nil, e
	}
	return &out, nil
}

// Approve consumes one exact authenticated owner approval step, without sending.
func (s *Service) Approve(ctx context.Context, in ApprovalInput) (*Action, error) {
	var out Action
	e := s.update(ctx, in.Scope.Key, func(st *State) error {
		if checkErr := s.owner(st, in.Scope); checkErr != nil {
			return checkErr
		}
		a := actionMap(st, in.Scope)[in.ActionID]
		if a == nil || a.Scope != in.Scope || a.Digest != in.Digest || a.State != "prepared" || in.Step != a.ApprovalSteps+1 || in.Step > a.RequiredSteps {
			return ErrStale
		}
		if checkErr := s.actionGuard(st, a); checkErr != nil {
			return checkErr
		}
		a.ApprovalSteps++
		if a.ApprovalSteps == a.RequiredSteps {
			a.State = "approved"
		}
		out = *a
		audit(st, s.now(), "approval", a.ID, a.State)
		return nil
	})
	if e != nil {
		return nil, e
	}
	return &out, nil
}

// Execute claims once under the persisted epoch before dispatch. An ambiguous
// transport result or a crash after this claim is never automatically replayed.
func (s *Service) Execute(ctx context.Context, scope Scope, id string) (result *Action, resultErr error) {
	started := time.Now()
	defer func() { s.observeResult("execute", "", started, resultErr) }()
	if !s.options.LiveWrites {
		return nil, ErrLiveWritesDisabled
	}
	if s.api == nil {
		return nil, ErrUnavailable
	}
	if checkErr := s.Reconcile(ctx, scope.Key); checkErr != nil {
		return nil, checkErr
	}
	st, e := s.read(ctx, scope.Key)
	if e != nil {
		return nil, e
	}
	candidate := actionMap(st, scope)[id]
	if candidate == nil || candidate.Scope != scope {
		return nil, ErrDenied
	}
	if checkErr := s.verifyAssets(ctx, candidate); checkErr != nil {
		return nil, checkErr
	}
	var request Action
	e = s.update(ctx, scope.Key, func(st *State) error {
		if checkErr := s.owner(st, scope); checkErr != nil {
			return checkErr
		}
		a := actionMap(st, scope)[id]
		if a == nil || a.Scope != scope || a.State != "approved" || a.ApprovalSteps != a.RequiredSteps {
			return ErrStale
		}
		if checkErr := s.actionGuard(st, a); checkErr != nil {
			return checkErr
		}
		if st.Connection.DispatchActionID != "" {
			return ErrUnavailable
		}
		a.State = "dispatching"
		a.DispatchAt = s.now()
		st.Connection.DispatchActionID = a.ID
		request = *a
		return nil
	})
	if e != nil {
		return nil, e
	}
	work, done := s.work(ctx, scope.Key, scope.PeerID)
	defer done()
	// The claim is the revoke barrier's admission point. Any later revocation
	// may cancel this already dispatching request but cannot promise unsending.
	current, e := s.read(work, scope.Key)
	if e != nil || s.actionGuard(current, &request) != nil {
		return nil, s.finishBeforeDispatchFailure(ctx, scope, &request, ErrStale)
	}
	if checkErr := s.verifyAssets(work, &request); checkErr != nil {
		return nil, s.finishBeforeDispatchFailure(ctx, scope, &request, checkErr)
	}
	if request.SourceKey != nil {
		if e = s.Reconcile(work, *request.SourceKey); e != nil {
			return nil, s.finishBeforeDispatchFailure(ctx, scope, &request, e)
		}
		source, sourceErr := s.read(work, *request.SourceKey)
		if sourceErr != nil || s.available(source, Scope{Key: *request.SourceKey, OwnerID: scope.OwnerID}) != nil || source.Connection.Epoch != request.SourceEpoch || !source.Connection.Policy.Enabled || !source.Connection.Policy.Consent || !source.Connection.Rights.CanManageStories {
			return nil, s.finishBeforeDispatchFailure(ctx, scope, &request, ErrStale)
		}
	}
	if _, e = (Request{Action: request}).Payload(); e != nil {
		return nil, s.finishBeforeDispatchFailure(ctx, scope, &request, e)
	}
	receipt, callErr := s.api.Execute(work, Request{Action: request})
	if callErr == nil && !validReceipt(request.Method, receipt) {
		callErr = ErrUnknownOutcome
	}
	if e = s.finish(ctx, scope, &request, &receipt, callErr, rejected(callErr)); e != nil {
		return nil, ErrUnknownOutcome
	}
	st, e = s.read(ctx, scope.Key)
	if e != nil {
		return nil, e
	}
	a := actionMap(st, scope)[id]
	if a == nil {
		return nil, ErrUnavailable
	}
	if a.State == "unknown" {
		return a, ErrUnknownOutcome
	}
	return a, callErr
}

func needsMessageReceipt(m Method) bool {
	return m == SendText || m == SendPhoto || m == SendDocument || m == SendVoice || m == SendAudio || m == SendVideo || m == EditText
}

func validReceipt(method Method, receipt Receipt) bool {
	if len(receipt.Result) > 65536 {
		return false
	}
	if needsMessageReceipt(method) {
		return len(receipt.MessageIDs) == 1 && receipt.MessageIDs[0] > 0
	}
	if method == PostStory || method == EditStory || method == RepostStory {
		return receipt.StoryID > 0
	}
	if method == GetStarBalance || method == GetGifts {
		var object map[string]json.RawMessage
		return json.Unmarshal(receipt.Result, &object) == nil && object != nil
	}
	var ok bool
	return json.Unmarshal(receipt.Result, &ok) == nil && ok
}

func (s *Service) finish(ctx context.Context, scope Scope, request *Action, receipt *Receipt, callErr error, definite bool) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return s.update(cleanup, scope.Key, func(st *State) error {
		if checkErr := s.owner(st, scope); checkErr != nil {
			return checkErr
		}
		a := actionMap(st, scope)[request.ID]
		if a == nil || a.Digest != request.Digest || a.State != "dispatching" {
			return ErrStale
		}
		if callErr != nil {
			a.State = "unknown"
			a.Failure = "unknown_outcome"
			if definite {
				a.State = "failed"
				a.Failure = "rejected"
			}
		} else {
			a.State = "sent"
			a.Receipt = receipt
			staleContext := st.Connection.Epoch != request.Epoch || st.Connection.Quarantined || !st.Connection.Policy.Consent || scope.PeerID > 0 && (st.Peers[scope.PeerID] == nil || st.Peers[scope.PeerID].Revision != request.ContextRevision || !st.Peers[scope.PeerID].Policy.Consent)
			if staleContext {
				a.Receipt = &Receipt{MessageIDs: receipt.MessageIDs, StoryID: receipt.StoryID}
			}
			if !staleContext && scope.PeerID > 0 && needsMessageReceipt(request.Method) {
				p := st.Peers[scope.PeerID]
				var text string
				field := "caption"
				if request.Method == SendText || request.Method == EditText {
					field = "text"
				}
				if raw := request.Params[field]; raw != nil {
					if decodeErr := json.Unmarshal(raw, &text); decodeErr != nil {
						return ErrUnknownOutcome
					}
				}
				text = security.RedactSensitiveText(text)
				for idIndex := range receipt.MessageIDs {
					id := receipt.MessageIDs[idIndex]
					found := false
					for i := range p.Messages {
						if p.Messages[i].ID != id {
							continue
						}
						p.Messages[i].Text = text
						p.Messages[i].Direction = "bot_outgoing"
						p.Messages[i].Revision++
						found = true
						break
					}
					if !found {
						p.Messages = append(p.Messages, Message{ID: id, Date: s.now(), Direction: "bot_outgoing", SenderID: st.Connection.OwnerTelegramID, Text: text})
					}
				}
				p.Summary = nil
				if request.Automatic {
					p.LastProcessingState = "ready"
					p.LastProcessingReason = "sent"
					p.LastProcessedAt = s.now()
				}
				invalidatePeer(p)
			}
		}
		if st.Connection.DispatchActionID == a.ID {
			st.Connection.DispatchActionID = ""
		}
		if a.DraftID != "" && scope.PeerID > 0 {
			if d := st.Peers[scope.PeerID].Drafts[a.DraftID]; d != nil {
				d.State = a.State
			}
		}
		audit(st, s.now(), "action", a.ID, a.State)
		return nil
	})
}

func (s *Service) verifyAssets(ctx context.Context, a *Action) error {
	if checkErr := s.validateMediaSources(ctx, a.Scope, a.Method, a.Params); checkErr != nil {
		return checkErr
	}
	for _, field := range []string{"photo", "document", "voice", "audio", "video", "content"} {
		raw, ok := a.Params[field]
		if !ok {
			continue
		}
		var file string
		if field == "content" || field == "photo" && a.Method == SetProfilePhoto {
			var p Params
			if json.Unmarshal(raw, &p) != nil {
				return ErrDenied
			}
			for _, f := range []string{"photo", "video"} {
				if v, ok := p[f]; ok {
					if json.Unmarshal(v, &file) != nil {
						return ErrDenied
					}
				}
			}
		} else if json.Unmarshal(raw, &file) != nil {
			return ErrDenied
		}
		if file == "" || s.options.VerifyMedia == nil {
			if strings.HasPrefix(file, "bmedia_") && s.options.Media != nil {
				continue
			}
			if a.Method == SetProfilePhoto || a.Method == PostStory || a.Method == EditStory {
				return ErrDenied
			}
			st, e := s.read(ctx, a.Scope.Key)
			if e != nil || s.available(st, a.Scope) != nil {
				return ErrUnavailable
			}
			p := st.Peers[a.Scope.PeerID]
			found := false
			if p != nil {
				for mIndex := range p.Messages {
					m := p.Messages[mIndex]
					if !m.Deleted {
						for mediaIndex := range m.Media {
							media := m.Media[mediaIndex]
							if media.FileID == file {
								found = true
							}
						}
					}
				}
			}
			if !found {
				return ErrDenied
			}
			continue
		}
		if checkErr := s.options.VerifyMedia(ctx, a.Scope, file); checkErr != nil {
			return checkErr
		}
	}
	return nil
}

func (s *Service) finishBeforeDispatchFailure(ctx context.Context, scope Scope, request *Action, cause error) error {
	if finishErr := s.finish(ctx, scope, request, nil, cause, true); finishErr != nil {
		return ErrUnknownOutcome
	}
	return cause
}

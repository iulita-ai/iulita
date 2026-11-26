package telegrambusiness

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Process is scheduled after durable intake, never inside the polling ACK path.
// One context revision gets one automatic generation attempt. Owner-triggered
// Draft is a separate explicit, budgeted retry. Default/off policies do nothing.
func (s *Service) Process(ctx context.Context, scope Scope) (resultErr error) {
	started := time.Now()
	defer func() { s.observeResult("process", "", started, resultErr) }()
	var last error
	for round := 0; round < 4; round++ {
		if checkErr := ctx.Err(); checkErr != nil {
			return checkErr
		}
		last = s.processOnce(ctx, scope)
		st, e := s.read(ctx, scope.Key)
		if e != nil {
			return last
		}
		if checkErr := s.available(st, scope); checkErr != nil {
			return last
		}
		p := st.Peers[scope.PeerID]
		if p == nil || p.ProcessRevision != p.Revision || p.AutoClaim != "" || p.AutoRevision == p.Revision || !st.Connection.Policy.Enabled || !st.Connection.Policy.Consent || !p.Policy.Enabled || !p.Policy.Consent || p.Policy.Provider == "" || p.Policy.Mode == ModeOff {
			return last
		}
		messages := contextMessages(p)
		if len(messages) == 0 || messages[len(messages)-1].Direction != "incoming" {
			return last
		}
	}
	return ErrQuota
}

func (s *Service) processOnce(ctx context.Context, scope Scope) (resultErr error) {
	claim := newID()
	if claim == "" {
		return ErrUnavailable
	}
	admitted := false
	var revision uint64
	var sourceAge time.Duration
	claimErr := s.update(ctx, scope.Key, func(st *State) error {
		if checkErr := s.available(st, scope); checkErr != nil {
			return checkErr
		}
		peer := st.Peers[scope.PeerID]
		if peer == nil || !st.Connection.Policy.Enabled || !st.Connection.Policy.Consent || !peer.Policy.Enabled || !peer.Policy.Consent || peer.Policy.Provider == "" || peer.Policy.Mode == ModeOff {
			return nil
		}
		if peer.ProcessRevision != peer.Revision || peer.AutoRevision == peer.Revision || peer.AutoClaim != "" {
			return nil
		}
		messages := contextMessages(peer)
		if len(messages) == 0 || messages[len(messages)-1].Direction != "incoming" {
			return nil
		}
		if !peer.LastIncomingAt.IsZero() {
			sourceAge = s.now().Sub(peer.LastIncomingAt)
		}
		revision = peer.Revision
		peer.AutoRevision = revision
		peer.AutoClaim = claim
		admitted = true
		return nil
	})
	if claimErr != nil || !admitted {
		return claimErr
	}
	s.observe(Observation{Operation: "queue", Kind: "candidate", Outcome: "ok", Age: sourceAge})
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		cleanupErr := s.update(cleanup, scope.Key, func(st *State) error {
			if ownerErr := s.owner(st, scope); ownerErr != nil {
				return ownerErr
			}
			if p := st.Peers[scope.PeerID]; p != nil && p.AutoClaim == claim {
				if p.Revision == revision {
					p.LastProcessingState, p.LastProcessingReason = processingOutcome(resultErr)
					p.LastProcessedAt = s.now()
				}
				p.AutoClaim = ""
			}
			return nil
		})
		if cleanupErr != nil && resultErr == nil {
			resultErr = ErrUnavailable
		}
	}()
	draft, draftErr := s.DraftRevision(ctx, scope, revision)
	if draftErr != nil {
		return draftErr
	}
	st, p, _, contextErr := s.scopedContext(ctx, scope)
	if contextErr != nil {
		return contextErr
	}
	if p.Revision != revision || p.AutoClaim != claim {
		return ErrStale
	}
	if !s.options.LiveWrites || st.Connection.Policy.Mode != ModeAuto || !st.Connection.Policy.AutoReply || p.Policy.Mode != ModeAuto || !p.Policy.AutoReply {
		return nil
	}
	if s.autoAllowed(st, scope, "") != nil {
		return nil
	}
	text, marshalErr := json.Marshal(draft.Text)
	if marshalErr != nil {
		return ErrUnavailable
	}
	action, prepareErr := s.Prepare(ctx, PrepareInput{Scope: scope, Method: SendText, Params: Params{"text": text}, DraftID: draft.ID})
	if prepareErr != nil {
		return prepareErr
	}
	// Auto approval derives exclusively from the explicit persisted narrow
	// owner policy; peer text cannot select an operation or approve a mutation.
	approvalErr := s.update(ctx, scope.Key, func(current *State) error {
		peer := current.Peers[scope.PeerID]
		if peer == nil || peer.Revision != revision || peer.AutoClaim != claim || current.Connection.Policy.Mode != ModeAuto || !current.Connection.Policy.AutoReply || peer.Policy.Mode != ModeAuto || !peer.Policy.AutoReply {
			return ErrStale
		}
		a := peer.Actions[action.ID]
		if checkErr := s.autoAllowed(current, scope, action.ID); checkErr != nil {
			return checkErr
		}
		a.Automatic = true
		a.Digest = digestAction(*a)
		if checkErr := s.actionGuard(current, a); checkErr != nil {
			return checkErr
		}
		if a.RequiredSteps != 1 || a.Method != SendText {
			return ErrDenied
		}
		a.ApprovalSteps = 1
		a.State = "approved"
		audit(current, s.now(), "auto_policy", a.ID, "approved")
		return nil
	})
	if approvalErr != nil {
		return approvalErr
	}
	_, executeErr := s.Execute(ctx, scope, action.ID)
	return executeErr
}

func processingOutcome(err error) (state, reason string) {
	switch {
	case err == nil:
		return "ready", "draft_ready"
	case errors.Is(err, ErrStale):
		return "skipped", "stale_context"
	case errors.Is(err, context.Canceled):
		return "skipped", "cancelled"
	case errors.Is(err, ErrQuota):
		return "failed", "budget_exhausted"
	case errors.Is(err, ErrUnsupported):
		return "failed", "unsupported"
	case errors.Is(err, ErrDenied):
		return "failed", "forbidden"
	case errors.Is(err, ErrUnknownOutcome):
		return "failed", "unknown_outcome"
	case errors.Is(err, ErrUnavailable), errors.Is(err, context.DeadlineExceeded):
		return "failed", "unavailable"
	default:
		return "failed", "processing_failed"
	}
}

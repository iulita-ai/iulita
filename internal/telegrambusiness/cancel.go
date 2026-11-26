package telegrambusiness

import "context"

// CancelAction cancels an exact un-dispatched intent. An uncertain/in-flight
// result is never relabelled as definitely cancelled or refunded automatically.
func (s *Service) CancelAction(ctx context.Context, scope Scope, id, digest string) error {
	return s.update(ctx, scope.Key, func(st *State) error {
		if checkErr := s.owner(st, scope); checkErr != nil {
			return checkErr
		}
		a := actionMap(st, scope)[id]
		if a == nil || a.Scope != scope || a.Digest != digest {
			return ErrStale
		}
		if a.State == "dispatching" || a.State == "unknown" {
			return ErrUnknownOutcome
		}
		if a.State != "prepared" && a.State != "approved" {
			return ErrStale
		}
		a.State = "cancelled"
		audit(st, s.now(), "cancel", id, "cancelled")
		return nil
	})
}

// RejectDraft removes an owner-rejected exact draft and its pending approval.
func (s *Service) RejectDraft(ctx context.Context, scope Scope, id string, revision uint64) error {
	return s.update(ctx, scope.Key, func(st *State) error {
		if checkErr := s.owner(st, scope); checkErr != nil {
			return checkErr
		}
		p := st.Peers[scope.PeerID]
		if p == nil {
			return ErrUnavailable
		}
		d := p.Drafts[id]
		if d == nil || d.Revision != revision {
			return ErrStale
		}
		if a := p.Actions[d.ActionID]; a != nil {
			if a.State == "dispatching" || a.State == "unknown" {
				return ErrUnknownOutcome
			}
			if a.State == "sent" {
				return ErrStale
			}
			a.State = "cancelled"
			a.Params = nil
		}
		delete(p.Drafts, id)
		p.OwnerDraftRevision++
		audit(st, s.now(), "reject", id, "cancelled")
		return nil
	})
}

package telegrambusiness

import (
	"context"
	"time"
)

// Rebind is an explicit fresh owner-control reset after quarantine/security
// epoch change or restore. It discards old private state and all old policies;
// a separate Configure consent is required before retaining new content.
func (s *Service) Rebind(ctx context.Context, owner string, key Key, expectedEpoch uint64) error {
	if s.api == nil || owner == "" || key.Validate() != nil {
		return ErrDenied
	}
	work, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	remote, e := s.api.GetConnection(work, key)
	if e != nil {
		return e
	}
	if remote.Key != key || !remote.Enabled || remote.OwnerTelegramID <= 0 || remote.OwnerChatID <= 0 {
		return ErrDenied
	}
	e = s.update(work, key, func(st *State) error {
		scope := Scope{Key: key, OwnerID: owner}
		if checkErr := s.owner(st, scope); checkErr != nil {
			return checkErr
		}
		if st.Connection.Epoch != expectedEpoch || st.Connection.OwnerTelegramID != remote.OwnerTelegramID || st.Connection.OwnerChatID != remote.OwnerChatID {
			return ErrStale
		}
		for pIndex := range st.Peers {
			p := st.Peers[pIndex]
			p.Summary = nil
			p.AutoClaim = ""
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
				if a.State == "dispatching" {
					a.State = "unknown"
				} else if a.State != "unknown" && a.State != "sent" {
					a.State = "cancelled"
				}
			}
			p.Policy = PeerPolicy{Mode: ModeOff, Revision: p.Policy.Revision + 1}
			invalidatePeer(p)
		}
		for aIndex := range st.AccountActions {
			a := st.AccountActions[aIndex]
			a.Params = nil
			a.Receipt = nil
			if a.State == "dispatching" {
				a.State = "unknown"
			} else if a.State != "unknown" && a.State != "sent" {
				a.State = "cancelled"
			}
		}
		st.Connection.RemoteConnection = remote
		st.Connection.Policy = PeerPolicy{Mode: ModeOff, Revision: st.Connection.Policy.Revision + 1}
		st.Connection.Quarantined = false
		st.Connection.Paused = false
		st.Connection.DispatchActionID = ""
		st.Connection.RestoreGeneration = s.options.RestoreGeneration
		st.Connection.RuntimeGeneration = s.options.RuntimeGeneration
		st.Connection.NeedsRefresh = false
		st.Connection.VerifiedAt = s.now()
		invalidate(st)
		audit(st, s.now(), "rebind", "", "reset")
		return nil
	})
	if e == nil {
		s.cancelKey(key)
	}
	return e
}

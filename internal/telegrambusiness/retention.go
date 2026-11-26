package telegrambusiness

import (
	"context"
	"sort"
	"time"
)

func (s *Service) update(ctx context.Context, key Key, fn func(*State) error) error {
	if s.repo == nil || key.Validate() != nil {
		return ErrUnavailable
	}
	if checkErr := ctx.Err(); checkErr != nil {
		return checkErr
	}
	return s.repo.Update(ctx, key, func(st *State) error {
		initState(st)
		prune(st, s.now())
		if checkErr := fn(st); checkErr != nil {
			return checkErr
		}
		eraseDeletedSourceData(st)
		return nil
	})
}

func (s *Service) read(ctx context.Context, key Key) (*State, error) {
	if s.repo == nil || key.Validate() != nil {
		return nil, ErrUnavailable
	}
	if checkErr := ctx.Err(); checkErr != nil {
		return nil, checkErr
	}
	st, e := s.repo.Get(ctx, key)
	if e != nil {
		return nil, e
	}
	if st == nil {
		return nil, ErrUnavailable
	}
	initState(st)
	prune(st, s.now())
	return st, nil
}

// prune applies source-age retention even when fresh connection events keep the
// row itself current. All discarded content remains unavailable to retrieval.
func prune(st *State, now time.Time) {
	eraseDeletedSourceData(st)
	cut := now.Add(-7 * 24 * time.Hour)
	tomb := now.Add(-90 * 24 * time.Hour)
	pruneActions := func(actions map[string]*Action) {
		for id, a := range actions {
			if a == nil {
				delete(actions, id)
				continue
			}
			if (a.State == "prepared" || a.State == "approved") && !a.ExpiresAt.After(now) {
				a.State = "expired"
				a.Params = nil
				a.Receipt = nil
			}
			if a.CreatedAt.Before(cut) {
				a.Params = nil
				if a.Receipt != nil {
					a.Receipt.Result = nil
				}
				if a.State != "dispatching" && a.State != "sent" && a.State != "unknown" {
					a.State = "expired"
				}
			}
			if a.CreatedAt.Before(tomb) && a.State != "dispatching" {
				delete(actions, id)
			}
		}
		// Only replaceable unclaimed/definitely-failed previews are trimmed. Unknown,
		// sent and dispatching financial/reply ledgers are never refunded by trimming.
		if len(actions) >= 5000 {
			closed := make([]*Action, 0, len(actions))
			for aIndex := range actions {
				a := actions[aIndex]
				if a != nil && (a.State == "cancelled" || a.State == "expired" || a.State == "failed") {
					closed = append(closed, a)
				}
			}
			sort.Slice(closed, func(i, j int) bool {
				if closed[i].CreatedAt.Equal(closed[j].CreatedAt) {
					return closed[i].ID < closed[j].ID
				}
				return closed[i].CreatedAt.Before(closed[j].CreatedAt)
			})
			for aIndex := range closed {
				a := closed[aIndex]
				if len(actions) < 5000 {
					break
				}
				delete(actions, a.ID)
			}
		}
	}
	pruneActions(st.AccountActions)
	for id, p := range st.Peers {
		if p == nil {
			delete(st.Peers, id)
			continue
		}
		if p.Drafts == nil {
			p.Drafts = map[string]*Draft{}
		}
		if p.Actions == nil {
			p.Actions = map[string]*Action{}
		}
		changed := false
		out := p.Messages[:0]
		for mIndex := range p.Messages {
			m := p.Messages[mIndex]
			if m.Date.Before(cut) && (m.Text != "" || len(m.Media) > 0 || m.MediaGroupID != "" || m.ExtractedText != "") {
				m.Text = ""
				m.Media = nil
				m.MediaGroupID = ""
				m.ExtractedText = ""
				m.ExtractedProvider = ""
				m.Deleted = true
				changed = true
			}
			if !m.Date.Before(tomb) {
				out = append(out, m)
			}
		}
		p.Messages = out
		for id, d := range p.Drafts {
			if d == nil {
				delete(p.Drafts, id)
				continue
			}
			if !d.ExpiresAt.After(now) || d.CreatedAt.Before(now.Add(-24*time.Hour)) {
				d.Text = ""
				if d.State != "sent" {
					d.State = "expired"
				}
			}
			if d.CreatedAt.Before(tomb) {
				delete(p.Drafts, id)
			}
		}
		if changed {
			invalidatePeer(p)
			clearDerived(p)
		}
		if p.Summary != nil && !validSummary(p) {
			p.Summary = nil
		}
		pruneActions(p.Actions)
	}
	for id, at := range st.Seen {
		if at.Before(now.Add(-24 * time.Hour)) {
			delete(st.Seen, id)
		}
	}
	out := st.Audit[:0]
	for aIndex := range st.Audit {
		a := st.Audit[aIndex]
		if !a.At.Before(now.Add(-30 * 24 * time.Hour)) {
			out = append(out, a)
		}
	}
	st.Audit = out
}

// PruneState is the pure source-age cleanup used by trusted storage-only erase
// transactions too. It performs no I/O and does not grant read/delivery access.
func PruneState(st *State, now time.Time) {
	if st != nil {
		initState(st)
		prune(st, now)
	}
}

func clearDerived(p *Peer) {
	p.Summary = nil
	for dIndex := range p.Drafts {
		d := p.Drafts[dIndex]
		if d != nil {
			d.Text = ""
			d.State = "cancelled"
		}
	}
	for aIndex := range p.Actions {
		a := p.Actions[aIndex]
		if a != nil {
			a.Params = nil
			if a.Receipt != nil {
				a.Receipt.Result = nil
			}
			if a.State != "dispatching" && a.State != "sent" && a.State != "unknown" {
				a.State = "cancelled"
			}
		}
	}
}

func restartFence(st *State) {
	for pIndex := range st.Peers {
		p := st.Peers[pIndex]
		for aIndex := range p.Actions {
			a := p.Actions[aIndex]
			if a.State == "dispatching" {
				a.State = "unknown"
				a.Failure = "restart_unknown"
			}
		}
		p.AutoClaim = ""
	}
	for aIndex := range st.AccountActions {
		a := st.AccountActions[aIndex]
		if a.State == "dispatching" {
			a.State = "unknown"
			a.Failure = "restart_unknown"
		}
	}
	st.Connection.DispatchActionID = ""
}

func eraseDeletedSourceData(st *State) {
	for pIndex := range st.Peers {
		p := st.Peers[pIndex]
		if p == nil {
			continue
		}
		for i := range p.Messages {
			m := &p.Messages[i]
			if m.Deleted {
				m.Text = ""
				m.Media = nil
				m.MediaGroupID = ""
				m.ExtractedText = ""
				m.ExtractedProvider = ""
			}
		}
		if p.Summary != nil && !validSummary(p) {
			p.Summary = nil
		}
	}
}
func eraseMediaDerived(p *Peer) {
	if p == nil {
		return
	}
	p.Summary = nil
	p.LastMediaOmitted = 0
	for i := range p.Messages {
		p.Messages[i].ExtractedText = ""
		p.Messages[i].ExtractedProvider = ""
	}
}

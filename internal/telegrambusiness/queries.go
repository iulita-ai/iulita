package telegrambusiness

import (
	"context"
	"encoding/json"
	"sort"
)

// Drafts returns this peer's retained owner-authorized draft previews.
func (s *Service) Drafts(ctx context.Context, scope Scope) ([]Draft, error) {
	st, err := s.read(ctx, scope.Key)
	if err != nil {
		return nil, err
	}
	prune(st, s.now())
	if checkErr := s.owner(st, scope); checkErr != nil {
		return nil, checkErr
	}
	if st.Connection.Quarantined || s.options.RestoreGeneration != "" && st.Connection.RestoreGeneration != s.options.RestoreGeneration {
		return nil, ErrUnavailable
	}
	peer := st.Peers[scope.PeerID]
	if peer == nil {
		return []Draft{}, nil
	}
	out := make([]Draft, 0, len(peer.Drafts))
	for draftIndex := range peer.Drafts {
		draft := peer.Drafts[draftIndex]
		if draft != nil {
			out = append(out, *draft)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// Action returns a detached exact scoped operation.
func (s *Service) Action(ctx context.Context, scope Scope, id string) (*Action, error) {
	st, err := s.read(ctx, scope.Key)
	if err != nil {
		return nil, err
	}
	prune(st, s.now())
	if checkErr := s.owner(st, scope); checkErr != nil {
		return nil, checkErr
	}
	if st.Connection.Quarantined {
		return nil, ErrUnavailable
	}
	action := actionMap(st, scope)[id]
	if action == nil || action.Scope != scope {
		return nil, ErrUnavailable
	}
	// Copy nested Params/Receipt; callers cannot mutate repository-owned data.
	data, err := json.Marshal(action)
	if err != nil {
		return nil, ErrUnavailable
	}
	var cloned Action
	if json.Unmarshal(data, &cloned) != nil {
		return nil, ErrUnavailable
	}
	return &cloned, nil
}

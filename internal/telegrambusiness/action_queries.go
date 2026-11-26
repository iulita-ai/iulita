package telegrambusiness

import (
	"context"
	"encoding/json"
	"sort"
)

// Actions returns bounded owner-authorized operation history.
func (s *Service) Actions(ctx context.Context, scope Scope) ([]Action, error) {
	st, err := s.read(ctx, scope.Key)
	if err != nil {
		return nil, err
	}
	if checkErr := s.owner(st, scope); checkErr != nil {
		return nil, checkErr
	}
	if st.Connection.Quarantined {
		return nil, ErrUnavailable
	}
	out := make([]Action, 0)
	for _, action := range actionMap(st, scope) {
		if action == nil || action.Scope != scope {
			continue
		}
		data, err := json.Marshal(action)
		if err != nil {
			return nil, ErrUnavailable
		}
		var cloned Action
		if json.Unmarshal(data, &cloned) != nil {
			return nil, ErrUnavailable
		}
		out = append(out, cloned)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	if len(out) > 100 {
		out = out[len(out)-100:]
	}
	return out, nil
}

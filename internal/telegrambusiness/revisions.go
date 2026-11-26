package telegrambusiness

import "context"

func expectedScope(st *State, scope Scope, expected *uint64) error {
	if expected == nil {
		return nil
	}
	revision := st.Connection.Epoch
	if scope.PeerID > 0 {
		p := st.Peers[scope.PeerID]
		if p == nil {
			return ErrStale
		}
		revision = p.Revision
	}
	if revision != *expected {
		return ErrStale
	}
	return nil
}

// PurgeRevision atomically binds a destructive owner action to its preview.
func (s *Service) PurgeRevision(ctx context.Context, scope Scope, expected uint64) error {
	return s.purge(ctx, scope, &expected)
}

// CreateDraftRevision prevents a stale editor replacing a newer owner context.
func (s *Service) CreateDraftRevision(ctx context.Context, scope Scope, text string, expected uint64) (*Draft, error) {
	return s.createDraft(ctx, scope, text, &expected)
}

// DraftRevision validates the owner preview before any billable work begins.
func (s *Service) DraftRevision(ctx context.Context, scope Scope, expected uint64) (*Draft, error) {
	return s.draft(ctx, scope, &expected)
}

// CompactRevision rejects a stale owner preview before any reserved model call.
func (s *Service) CompactRevision(ctx context.Context, scope Scope, expected uint64) (CompactResult, error) {
	return s.compact(ctx, scope, &expected)
}

package telegrambusiness

import "context"

// DispatchAllowed validates persisted claimed authority at the transport's last
// admission boundary. It performs no remote request, claim or callback replay.
func (s *Service) DispatchAllowed(ctx context.Context, request Request, deliveryGeneration string) error {
	if checkErr := ctx.Err(); checkErr != nil {
		return checkErr
	}
	st, e := s.read(ctx, request.Action.Scope.Key)
	if e != nil {
		return e
	}
	a := actionMap(st, request.Action.Scope)[request.Action.ID]
	if a == nil || a.Scope != request.Action.Scope || a.State != "dispatching" || st.Connection.DispatchActionID != a.ID || deliveryGeneration == "" || st.Connection.DeliveryGeneration != deliveryGeneration || a.Digest != request.Action.Digest || digestAction(request.Action) != a.Digest {
		return ErrStale
	}
	if checkErr := s.actionGuard(st, a); checkErr != nil {
		return checkErr
	}
	if a.SourceKey != nil {
		source, e := s.read(ctx, *a.SourceKey)
		if e != nil {
			return e
		}
		if s.available(source, Scope{Key: *a.SourceKey, OwnerID: a.Scope.OwnerID}) != nil || source.Connection.Epoch != a.SourceEpoch || !source.Connection.Policy.Enabled || !source.Connection.Policy.Consent || !source.Connection.Rights.CanManageStories {
			return ErrStale
		}
	}
	return nil
}

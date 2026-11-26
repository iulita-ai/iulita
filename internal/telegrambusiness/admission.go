package telegrambusiness

import "context"

// FenceDelivery closes persisted account authority before a replacement channel
// becomes visible to concurrent dashboard/provider requests. Reconciliation is a
// separate read-only network step after publication, never a DB transaction.
func (s *Service) FenceDelivery(ctx context.Context, key Key, generation string) error {
	if generation == "" || len(generation) > 128 {
		return ErrDenied
	}
	err := s.update(ctx, key, func(st *State) error {
		if st.Connection.OwnerID == "" {
			return ErrUnavailable
		}
		st.Connection.NeedsRefresh = true
		st.Connection.DeliveryGeneration = generation
		st.Connection.ControlUpdateID = -1
		invalidate(st)
		for pIndex := range st.Peers {
			p := st.Peers[pIndex]
			for aIndex := range p.Actions {
				a := p.Actions[aIndex]
				if a.State == "dispatching" {
					a.State = "unknown"
				}
			}
		}
		for aIndex := range st.AccountActions {
			a := st.AccountActions[aIndex]
			if a.State == "dispatching" {
				a.State = "unknown"
			}
		}
		st.Connection.DispatchActionID = ""
		return nil
	})
	if err == nil {
		s.cancelKey(key)
	}
	return err
}

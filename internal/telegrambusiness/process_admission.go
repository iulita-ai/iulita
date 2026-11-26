package telegrambusiness

import "context"

// AdmitProcessCandidate releases one current source revision only after the
// parent's successful durable cursor stage. No model or transport runs here.
func (s *Service) AdmitProcessCandidate(ctx context.Context, scope Scope, messageID int, revision int64, generation string, expected ...uint64) error {
	return s.update(ctx, scope.Key, func(st *State) error {
		if checkErr := s.available(st, scope); checkErr != nil {
			return checkErr
		}
		p := st.Peers[scope.PeerID]
		if generation == "" || st.Connection.DeliveryGeneration != generation || p == nil || !st.Connection.Policy.Enabled || !st.Connection.Policy.Consent || !p.Policy.Enabled || !p.Policy.Consent || p.Policy.Mode == ModeOff {
			return ErrDenied
		}
		if len(expected) > 1 || len(expected) == 1 && p.Revision != expected[0] {
			return ErrStale
		}
		messages := contextMessages(p)
		if len(messages) == 0 {
			return ErrStale
		}
		last := messages[len(messages)-1]
		if last.ID != messageID || last.Revision != revision || last.Direction != "incoming" || last.SenderIsBot {
			return ErrStale
		}
		p.ProcessRevision = p.Revision
		return nil
	})
}

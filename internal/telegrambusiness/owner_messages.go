package telegrambusiness

import "context"

// OwnerMessages is a local control-plane read. Pause stops processing/actions,
// not the authenticated owner's inspection of already retained correspondence.
// The processing path continues to use Messages and its stricter policy guard.
func (s *Service) OwnerMessages(ctx context.Context, scope Scope) ([]Message, error) {
	st, err := s.read(ctx, scope.Key)
	if err != nil {
		return nil, err
	}
	if checkErr := s.owner(st, scope); checkErr != nil {
		return nil, checkErr
	}
	if st.Connection.Quarantined || s.options.RestoreGeneration != "" && st.Connection.RestoreGeneration != s.options.RestoreGeneration {
		return nil, ErrUnavailable
	}
	peer := st.Peers[scope.PeerID]
	if peer == nil {
		return []Message{}, nil
	}
	out := make([]Message, 0, len(peer.Messages))
	for messageIndex := range peer.Messages {
		message := peer.Messages[messageIndex]
		if !message.Deleted {
			message.Media = append([]Media(nil), message.Media...)
			out = append(out, message)
		}
	}
	return out, nil
}

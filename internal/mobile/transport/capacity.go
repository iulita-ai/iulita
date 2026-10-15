package transport

func (s *Server) acquireOwner(owner string, stream bool) bool {
	s.ownerMu.Lock()
	defer s.ownerMu.Unlock()
	if s.owners[owner] >= 16 || (stream && s.streams[owner] >= 4) {
		return false
	}
	s.owners[owner]++
	if stream {
		s.streams[owner]++
	}
	return true
}
func (s *Server) releaseOwner(owner string, stream bool) {
	s.ownerMu.Lock()
	defer s.ownerMu.Unlock()
	s.owners[owner]--
	if s.owners[owner] == 0 {
		delete(s.owners, owner)
	}
	if stream {
		s.streams[owner]--
		if s.streams[owner] == 0 {
			delete(s.streams, owner)
		}
	}
}

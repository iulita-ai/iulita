package telegrambusiness

import (
	"strings"
	"time"
	"unicode/utf8"
)

func validateAutoRules(r *AutoRules) error {
	if r == nil {
		return nil
	}
	if len(r.Classes) == 0 || len(r.Classes) > 3 || r.MaxRepliesPerDay < 1 || r.MaxRepliesPerDay > 10 || r.StartHour < 0 || r.StartHour > 23 || r.EndHour < 0 || r.EndHour > 24 || r.StartHour == r.EndHour || r.Timezone == "" || len(r.Timezone) > 128 {
		return ErrDenied
	}
	if _, e := time.LoadLocation(r.Timezone); e != nil {
		return ErrDenied
	}
	seen := map[string]bool{}
	for cIndex := range r.Classes {
		c := r.Classes[cIndex]
		if seen[c] || c != "greeting" && c != "acknowledgement" && c != "short_question" {
			return ErrDenied
		}
		seen[c] = true
	}
	return nil
}

func autoClass(m Message) string {
	text := strings.TrimSpace(m.Text)
	if m.Deleted || m.Direction != "incoming" || m.SenderIsBot || len(m.Media) > 0 || strings.ContainsAny(text, "\r\n") || strings.HasPrefix(text, "/") || utf8.RuneCountInString(text) > 160 {
		return ""
	}
	normalized := strings.Trim(strings.ToLower(text), ".! ,🙂👋🙏")
	switch normalized {
	case "hi", "hello", "hey", "good morning", "good afternoon", "good evening", "привет", "здравствуйте", "добрый день", "доброе утро", "добрый вечер":
		return "greeting"
	case "thanks", "thank you", "ok", "okay", "understood", "got it", "спасибо", "благодарю", "ок", "хорошо", "понятно", "принято":
		return "acknowledgement"
	}
	if strings.HasSuffix(text, "?") && strings.Count(text, "?") == 1 && len(text) > 1 {
		return "short_question"
	}
	return ""
}
func autoRuleAllows(r *AutoRules, class string, now time.Time) bool {
	if r == nil || validateAutoRules(r) != nil {
		return false
	}
	found := false
	for cIndex := range r.Classes {
		c := r.Classes[cIndex]
		if c == class {
			found = true
		}
	}
	if !found {
		return false
	}
	zone, e := time.LoadLocation(r.Timezone)
	if e != nil {
		return false
	}
	hour := now.In(zone).Hour()
	if r.StartHour < r.EndHour {
		return hour >= r.StartHour && hour < r.EndHour
	}
	return hour >= r.StartHour || hour < r.EndHour
}

// autoAllowed is evaluated again when approving and dispatching, so queued
// automatic work cannot cross a quiet period or refund an uncertain outcome.
func (s *Service) autoAllowed(st *State, scope Scope, exclude string) error {
	p := st.Peers[scope.PeerID]
	if p == nil || scope.PeerID <= 0 || st.Connection.Policy.Mode != ModeAuto || !st.Connection.Policy.AutoReply || p.Policy.Mode != ModeAuto || !p.Policy.AutoReply {
		return ErrDenied
	}
	if len(p.Messages) == 0 {
		return ErrDenied
	}
	latest := p.Messages[len(p.Messages)-1]
	class := autoClass(latest)
	now := s.now()
	conn, peer := st.Connection.Policy.AutoRules, p.Policy.AutoRules
	if class == "" || !autoRuleAllows(conn, class, now) || !autoRuleAllows(peer, class, now) {
		return ErrDenied
	}
	replyCap := conn.MaxRepliesPerDay
	if peer.MaxRepliesPerDay < replyCap {
		replyCap = peer.MaxRepliesPerDay
	}
	today := now.Truncate(24 * time.Hour)
	total, peerTotal := 0, 0
	for candidateIndex := range st.Peers {
		candidate := st.Peers[candidateIndex]
		if candidate == nil {
			continue
		}
		for aIndex := range candidate.Actions {
			a := candidate.Actions[aIndex]
			if a == nil || a.ID == exclude || !a.Automatic || a.State != "dispatching" && a.State != "unknown" && a.State != "sent" {
				continue
			}
			at := a.DispatchAt
			if at.IsZero() {
				at = a.CreatedAt
			}
			if at.Before(today) {
				continue
			}
			total++
			if candidate.ID == scope.PeerID {
				peerTotal++
			}
		}
	}
	if total >= conn.MaxRepliesPerDay || peerTotal >= replyCap {
		return ErrQuota
	}
	return nil
}

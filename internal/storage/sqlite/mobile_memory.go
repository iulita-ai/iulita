package sqlite

import (
	"context"
	"strings"
)

// ReadMobileSavedKnowledge is an explicit account-memory snapshot, never a
// mobile namespace opt-out. LIMIT and substring caps apply in SQL, before any
// large legacy row enters RAM. No history search or memory writes are performed.
func (s *Store) ReadMobileSavedKnowledge(ctx context.Context, owner string) (string, error) {
	var out strings.Builder
	queries := []string{
		`SELECT substr(content,1,1024) FROM directives WHERE user_id=? AND ` + legacyChatPredicate + ` ORDER BY id DESC LIMIT 1`,
		`SELECT substr(content,1,1024) FROM facts WHERE user_id=? AND ` + legacyChatPredicate + ` ORDER BY id DESC LIMIT 8`,
		`SELECT substr(content,1,1024) FROM insights WHERE user_id=? AND (expires_at IS NULL OR expires_at>CURRENT_TIMESTAMP) AND ` + legacyChatPredicate + ` ORDER BY id DESC LIMIT 4`,
		`SELECT substr(key||': '||value,1,1024) FROM tech_facts WHERE user_id=? AND ` + legacyChatPredicate + ` ORDER BY id DESC LIMIT 20`,
	}
	for _, query := range queries {
		var values []string
		if err := s.db.NewRaw(query, owner).Scan(ctx, &values); err != nil {
			return "", err
		}
		for _, value := range values {
			if out.Len()+len(value)+1 <= 24<<10 {
				out.WriteString(value)
				out.WriteByte('\n')
			}
		}
	}
	return out.String(), nil
}

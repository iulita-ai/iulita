package sqlite

import (
	"context"
	"strings"
)

// ReadMobileRelevantKnowledge augments the bounded baseline with FTS matches
// from this owner's saved facts. Legacy namespace exclusions stay in force.
func (s *Store) ReadMobileRelevantKnowledge(ctx context.Context, owner, query string) (string, error) {
	baseline, err := s.ReadMobileSavedKnowledge(ctx, owner)
	if err != nil {
		return "", err
	}
	words := strings.Fields(query)
	if len(words) > 32 {
		words = words[:32]
	}
	match := sanitizeFTS5Query(strings.Join(words, " "))
	if match == "" {
		return baseline, nil
	}
	var values []string
	if checkErr := s.db.NewRaw(`SELECT substr(content,1,1024) FROM facts WHERE user_id=? AND `+legacyChatPredicate+` AND id IN (SELECT rowid FROM facts_fts WHERE facts_fts MATCH ?) ORDER BY last_accessed_at DESC,id DESC LIMIT 4`, owner, match).Scan(ctx, &values); checkErr != nil {
		return "", checkErr
	}
	for _, value := range values {
		if len(baseline)+len(value)+1 <= 24<<10 {
			baseline += value + "\n"
		}
	}
	return baseline, nil
}

// ReadMobileSavedKnowledge is an explicit account-memory snapshot, never a
// mobile namespace opt-out. LIMIT and substring caps apply in SQL, before any
// large legacy row enters RAM. No history search or memory writes are performed.
func (s *Store) ReadMobileSavedKnowledge(ctx context.Context, owner string) (string, error) {
	var out strings.Builder
	queries := []string{
		`SELECT CASE WHEN length(CAST(content AS BLOB))<=16384 THEN content ELSE substr(content,1,4096) END FROM (SELECT user_id,content,updated_at,1 priority FROM account_directives UNION ALL SELECT user_id,content,updated_at,0 priority FROM directives WHERE ` + legacyChatPredicate + `) WHERE user_id=? ORDER BY priority DESC,updated_at DESC LIMIT 1`,
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

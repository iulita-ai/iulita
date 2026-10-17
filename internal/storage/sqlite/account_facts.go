package sqlite

import (
	"context"
	"errors"
)

// DeleteAccountFact deletes exactly one owned saved fact, regardless of source.
func (s *Store) DeleteAccountFact(ctx context.Context, owner string, id int64) (bool, error) {
	if owner == "" || id <= 0 {
		return false, errors.New("invalid account fact")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM facts WHERE user_id=? AND id=? AND `+legacyChatPredicate, owner, id)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// DeleteAccountFactsByQuery retains the legacy FTS semantics within one owner.
// Empty/operator-only queries never become an unfiltered DELETE.
func (s *Store) DeleteAccountFactsByQuery(ctx context.Context, owner, query string) (int64, error) {
	if owner == "" || len(query) > 2000 {
		return 0, errors.New("invalid account fact query")
	}
	match := sanitizeFTS5Query(query)
	if match == "" {
		return 0, nil
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM facts WHERE user_id=? AND `+legacyChatPredicate+` AND id IN (SELECT rowid FROM facts_fts WHERE facts_fts MATCH ?)`, owner, match)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

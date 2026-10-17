package sqlite

import (
	"context"
	"errors"
	"unicode/utf8"
)

// ReadAccountDirective returns shared instructions with legacy-source fallback.
func (s *Store) ReadAccountDirective(ctx context.Context, owner string) (string, error) {
	d, err := s.GetDirectiveByUser(ctx, owner)
	if err != nil {
		return "", err
	}
	if d == nil {
		return "", nil
	}
	return d.Content, nil
}

// WriteAccountDirective replaces the owner preference, retaining empty clears.
func (s *Store) WriteAccountDirective(ctx context.Context, owner, content string) error {
	if owner == "" || !utf8.ValidString(content) || len(content) > 16<<10 {
		return errors.New("invalid account directive")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO account_directives(user_id,content,updated_at) VALUES(?,?,CURRENT_TIMESTAMP) ON CONFLICT(user_id) DO UPDATE SET content=excluded.content,updated_at=excluded.updated_at`, owner, content)
	return err
}

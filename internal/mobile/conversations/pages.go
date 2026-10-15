package conversations

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/iulita-ai/iulita/internal/mobile/securetoken"
)

type pageCursor struct {
	Owner, Conversation, AfterID            string
	Generation, AfterSeq, HighSeq, Revision uint64
	Archived                                bool
	Expires                                 int64
}

func pageSize(n int) (int, error) {
	if n == 0 {
		n = 50
	}
	if n < 1 || n > 100 {
		return 0, ErrInvalid
	}
	return n, nil
}

// List returns an owner-scoped conversation page.
func (r *Repository) List(ctx context.Context, owner string, archived bool, limit int, token string) ([]Conversation, string, error) {
	if !validID(owner) {
		return nil, "", ErrNotFound
	}
	limit, err := pageSize(limit)
	if err != nil {
		return nil, "", err
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	var exists int
	if checkErr1 := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id=?`, owner).Scan(&exists); checkErr1 != nil {
		return nil, "", checkErr1
	}
	if exists != 1 {
		return nil, "", ErrNotFound
	}
	var rev uint64
	err = tx.QueryRowContext(ctx, `SELECT revision FROM mobile_conversation_catalog WHERE owner_id=?`, owner).Scan(&rev)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, "", err
	}
	c := pageCursor{Owner: owner, Revision: rev, Archived: archived, Expires: time.Now().Add(15 * time.Minute).Unix()}
	if token != "" {
		if checkErr2 := r.cursors.Open("conversation-list.v1", token, &c); checkErr2 != nil {
			return nil, "", checkErr2
		}
		if c.Owner != owner || c.Archived != archived || c.Revision != rev || c.Expires <= time.Now().Unix() {
			return nil, "", securetoken.ErrCursor
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM mobile_conversations WHERE owner_id=? AND (? OR archived=0) AND id>? ORDER BY id LIMIT ?`, owner, archived, c.AfterID, limit+1)
	if err != nil {
		return nil, "", err
	}
	var ids []string
	defer rows.Close() //nolint:errcheck // Fallback cleanup; explicit close below precedes writes.
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
			return nil, "", err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
	if err != nil {
		return nil, "", err
	}
	more := len(ids) > limit
	if more {
		ids = ids[:limit]
	}
	out := make([]Conversation, 0, len(ids))
	for _, id := range ids {
		v, e := getFull(ctx, tx, owner, id)
		if e != nil {
			return nil, "", e
		}
		out = append(out, v)
	}
	next := ""
	if more {
		c.AfterID = ids[len(ids)-1]
		next, err = r.cursors.Seal("conversation-list.v1", c)
		if err != nil {
			return nil, "", err
		}
	}
	if checkErr3 := tx.Commit(); checkErr3 != nil {
		return nil, "", checkErr3
	}
	return out, next, nil
}

// Messages pages are a fixed high-water cut of immutable messages. New appends
// do not enter subsequent pages; clear invalidates every cursor via generation.
func (r *Repository) Messages(ctx context.Context, owner, id string, generation uint64, limit int, token string) ([]Message, string, error) {
	if generation == 0 {
		return nil, "", ErrInvalid
	}
	limit, err := pageSize(limit)
	if err != nil {
		return nil, "", err
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	v, err := get(ctx, tx, owner, id)
	if err != nil {
		return nil, "", err
	}
	if v.Generation != generation {
		return nil, "", ErrConflict
	}
	c := pageCursor{Owner: owner, Conversation: id, Generation: generation, HighSeq: v.MessageSeq, Expires: time.Now().Add(15 * time.Minute).Unix()}
	if token != "" {
		if checkErr4 := r.cursors.Open("message-list.v1", token, &c); checkErr4 != nil {
			return nil, "", checkErr4
		}
		if c.Owner != owner || c.Conversation != id || c.Generation != generation || c.Expires <= time.Now().Unix() || c.HighSeq > v.MessageSeq {
			return nil, "", securetoken.ErrCursor
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,turn_id,turn_seq,message_seq,role,text,created_at FROM mobile_messages WHERE owner_id=? AND conversation_id=? AND generation=? AND message_seq>? AND message_seq<=? ORDER BY message_seq LIMIT ?`, owner, id, generation, c.AfterSeq, c.HighSeq, limit+1)
	if err != nil {
		return nil, "", err
	}
	out := make([]Message, 0)
	bytes := 0
	bounded := false
	defer rows.Close() //nolint:errcheck // Fallback cleanup; explicit close below precedes writes.
	for rows.Next() {
		m := Message{ConversationID: id, Generation: generation}
		var created int64
		if err = rows.Scan(&m.ID, &m.TurnID, &m.TurnSeq, &m.MessageSeq, &m.Role, &m.Text, &created); err != nil {
			rows.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
			return nil, "", err
		}
		m.CreatedAt = time.UnixMilli(created).UTC()
		if len(out) > 0 && bytes+len(m.Text)+256 > 1536<<10 {
			bounded = true
			break
		}
		bytes += len(m.Text) + 256
		out = append(out, m)
	}
	err = rows.Err()
	rows.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit || bounded {
		if len(out) > limit {
			out = out[:limit]
		}
		c.AfterSeq = out[len(out)-1].MessageSeq
		next, err = r.cursors.Seal("message-list.v1", c)
		if err != nil {
			return nil, "", err
		}
	}
	if checkErr5 := tx.Commit(); checkErr5 != nil {
		return nil, "", checkErr5
	}
	return out, next, nil
}

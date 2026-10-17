package conversations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/iulita-ai/iulita/internal/domain"
)

var savedKnowledgePredicate = fmt.Sprintf("lower(substr(chat_id,1,%d)) <> '%s'", len(domain.MobileChatPrefix), domain.MobileChatPrefix)

// KnowledgeInput has no owner/chat override. Identity comes only from the turn
// fence. Unknown fields are rejected by the native adapter before dispatch.
type KnowledgeInput struct {
	Action  string `json:"action"`
	Content string `json:"content"`
	Query   string `json:"query"`
	ID      int64  `json:"id"`
	Limit   int    `json:"limit"`
}
type knowledgeFact struct {
	ID      int64  `json:"id"`
	Content string `json:"content"`
}

// AccountKnowledge operates only on saved account facts under an accepted
// ACCOUNT turn. Writes and their exact tool outcome commit atomically. It does
// not call legacy chat APIs, emit global events or start memory extraction.
func (r *Repository) AccountKnowledge(ctx context.Context, f Fence, operation, name string, in KnowledgeInput) (string, error) {
	if operation == "" || !utf8.ValidString(in.Content+in.Query) || len(in.Content) > 16<<10 || len(in.Query) > 2000 {
		return "", ErrInvalid
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback() //nolint:errcheck // Preserve operation result.
	if fenceErr := r.fence(ctx, tx, f); fenceErr != nil {
		return "", fenceErr
	}
	var mode string
	if checkErr := tx.QueryRowContext(ctx, `SELECT memory_mode FROM mobile_turns WHERE owner_id=? AND id=?`, f.OwnerID, f.TurnID).Scan(&mode); checkErr != nil {
		return "", checkErr
	}
	if mode != "account" {
		return "", ErrInvalid
	}
	var state, outcome string
	if checkErr := tx.QueryRowContext(ctx, `SELECT state,outcome FROM mobile_tool_operations WHERE owner_id=? AND turn_id=? AND id=? AND processing_epoch=?`, f.OwnerID, f.TurnID, operation, f.ProcessingEpoch).Scan(&state, &outcome); checkErr != nil {
		return "", checkErr
	}
	if state == "succeeded" {
		return outcome, tx.Commit()
	}
	if state != "started" {
		return "", ErrConflict
	}
	var result any
	switch name {
	case "directives":
		switch in.Action {
		case "set", "clear":
			content := in.Content
			if in.Action == "clear" {
				content = ""
			} else if strings.TrimSpace(content) == "" {
				return "", ErrInvalid
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO account_directives(user_id,content,updated_at) VALUES(?,?,CURRENT_TIMESTAMP) ON CONFLICT(user_id) DO UPDATE SET content=excluded.content,updated_at=excluded.updated_at`, f.OwnerID, content); err != nil {
				return "", err
			}
			result = struct {
				Status string `json:"status"`
			}{"saved"}
		case "get":
			var content string
			err = tx.QueryRowContext(ctx, `SELECT substr(content,1,16000) FROM (SELECT user_id,content,updated_at,1 priority FROM account_directives UNION ALL SELECT user_id,content,updated_at,0 priority FROM directives WHERE `+savedKnowledgePredicate+`) WHERE user_id=? ORDER BY priority DESC,updated_at DESC LIMIT 1`, f.OwnerID).Scan(&content)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return "", err
			}
			result = struct {
				Content string `json:"content"`
			}{content}
		default:
			return "", ErrInvalid
		}
	case "remember":
		if strings.TrimSpace(in.Content) == "" {
			return "", ErrInvalid
		}
		var count int
		if checkErr := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM facts WHERE user_id=?`, f.OwnerID).Scan(&count); checkErr != nil {
			return "", checkErr
		}
		if count >= 10000 {
			return "", ErrQueueFull
		}
		var id int64
		err = tx.QueryRowContext(ctx, `SELECT id FROM facts WHERE user_id=? AND `+savedKnowledgePredicate+` AND content=? ORDER BY id LIMIT 1`, f.OwnerID, in.Content).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			err = tx.QueryRowContext(ctx, `INSERT INTO facts(chat_id,user_id,content,source_type,created_at,last_accessed_at,access_count) VALUES('',?,?,'user',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,0) RETURNING id`, f.OwnerID, in.Content).Scan(&id)
		}
		if err != nil {
			return "", err
		}
		result = struct {
			Status string `json:"status"`
			ID     int64  `json:"fact_id"`
		}{"remembered", id}
	case "recall":
		match := knowledgeQuery(in.Query)
		if match == "" {
			return "", ErrInvalid
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 10
		}
		if limit > 20 {
			limit = 20
		}
		rows, e := tx.QueryContext(ctx, `SELECT id,substr(content,1,2048) FROM facts WHERE user_id=? AND `+savedKnowledgePredicate+` AND id IN (SELECT rowid FROM facts_fts WHERE facts_fts MATCH ?) ORDER BY last_accessed_at DESC,id DESC LIMIT ?`, f.OwnerID, match, limit)
		if e != nil {
			return "", e
		}
		defer rows.Close() //nolint:errcheck // Fallback cleanup; explicit close precedes writes.
		facts := []knowledgeFact{}
		for rows.Next() {
			var fact knowledgeFact
			if e = rows.Scan(&fact.ID, &fact.Content); e != nil {
				break
			}
			facts = append(facts, fact)
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close() //nolint:errcheck,gosec // Rows error checked before close.
		if e != nil {
			return "", e
		}
		result = facts
	case "forget":
		// Delete exactly one stable ID. Query matches must first be resolved with
		// recall and confirmed per ID, so approval cannot drift with new facts.
		if in.ID <= 0 || strings.TrimSpace(in.Query) != "" {
			return "", ErrInvalid
		}
		res, e := tx.ExecContext(ctx, `DELETE FROM facts WHERE user_id=? AND `+savedKnowledgePredicate+` AND id=?`, f.OwnerID, in.ID)
		if e != nil {
			return "", e
		}
		n, e := res.RowsAffected()
		if e != nil {
			return "", e
		}
		result = struct {
			Status  string `json:"status"`
			Deleted int64  `json:"deleted"`
		}{"completed", n}
	default:
		return "", ErrInvalid
	}
	body, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	outcome = string(body)
	if _, err = tx.ExecContext(ctx, `UPDATE mobile_tool_operations SET state='succeeded',outcome=? WHERE owner_id=? AND turn_id=? AND id=? AND state='started' AND processing_epoch=?`, outcome, f.OwnerID, f.TurnID, operation, f.ProcessingEpoch); err != nil {
		return "", err
	}
	return outcome, tx.Commit()
}

// SearchCurrentHistory searches only previous turns of this generation. Shared
// account memory never implicitly joins other chats or legacy transcripts.
func (r *Repository) SearchCurrentHistory(ctx context.Context, f Fence, query string, limit int) (string, error) {
	if strings.TrimSpace(query) == "" || len(query) > 2000 {
		return "", ErrInvalid
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 20 {
		limit = 20
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback() //nolint:errcheck // Preserve operation result.
	if fenceErr := r.fence(ctx, tx, f); fenceErr != nil {
		return "", fenceErr
	}
	rows, err := tx.QueryContext(ctx, `SELECT role,substr(text,1,240) FROM mobile_messages WHERE owner_id=? AND conversation_id=? AND generation=? AND turn_seq<(SELECT turn_seq FROM mobile_turns WHERE owner_id=? AND id=?) AND instr(lower(text),lower(?))>0 ORDER BY message_seq DESC LIMIT ?`, f.OwnerID, f.ConversationID, f.Generation, f.OwnerID, f.TurnID, query, limit)
	if err != nil {
		return "", err
	}
	type hit struct {
		Role string `json:"role"`
		Text string `json:"text"`
	}
	defer rows.Close() //nolint:errcheck // Fallback cleanup; explicit close precedes commit.
	hits := []hit{}
	for rows.Next() {
		var h hit
		if err = rows.Scan(&h.Role, &h.Text); err != nil {
			break
		}
		hits = append(hits, h)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close() //nolint:errcheck,gosec // Rows error checked before close.
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(hits)
	if err != nil {
		return "", err
	}
	return string(body), tx.Commit()
}

func knowledgeQuery(query string) string {
	words := strings.Fields(query)
	if len(words) > 16 {
		words = words[:16]
	}
	for i, w := range words {
		words[i] = `"` + strings.ReplaceAll(w, `"`, `""`) + `"`
	}
	return strings.Join(words, " AND ")
}

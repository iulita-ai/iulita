package conversations

import (
	"context"
	"database/sql"
	"errors"
)

func migrate(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	// Never manufacture/import legacy accounts or migrate an arbitrary empty DB.
	var users int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&users); err != nil {
		return errors.New("existing Iulita users schema is required")
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS mobile_chat_schema(version INTEGER PRIMARY KEY CHECK(version=1))`,
		`INSERT INTO mobile_chat_schema SELECT 1 WHERE NOT EXISTS(SELECT 1 FROM mobile_chat_schema)`,
		`CREATE TABLE IF NOT EXISTS mobile_conversations(
 id TEXT PRIMARY KEY, owner_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 title TEXT NOT NULL, profile_id TEXT NOT NULL, memory_mode TEXT NOT NULL CHECK(memory_mode IN ('isolated','account')),
 version INTEGER NOT NULL DEFAULT 1 CHECK(version>0), generation INTEGER NOT NULL DEFAULT 1 CHECK(generation>0),
 archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN (0,1)), message_seq INTEGER NOT NULL DEFAULT 0,
 read_seq INTEGER NOT NULL DEFAULT 0 CHECK(read_seq>=0 AND read_seq<=message_seq),
 created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, UNIQUE(owner_id,id))`,
		`CREATE TABLE IF NOT EXISTS mobile_conversation_catalog(owner_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE, revision INTEGER NOT NULL CHECK(revision>0))`,
		`CREATE INDEX IF NOT EXISTS mobile_conversations_owner ON mobile_conversations(owner_id,id)`,
		// BE04 transcript binding only. BE05 adds admission/idempotency/queue state,
		// using this anchor; no public SendMessage implementation is supplied here.
		`CREATE TABLE IF NOT EXISTS mobile_transcript_turns(
 id TEXT PRIMARY KEY, owner_id TEXT NOT NULL, conversation_id TEXT NOT NULL, generation INTEGER NOT NULL,
 turn_seq INTEGER NOT NULL CHECK(turn_seq>0),
 FOREIGN KEY(owner_id,conversation_id) REFERENCES mobile_conversations(owner_id,id) ON DELETE CASCADE,
 UNIQUE(owner_id,conversation_id,generation,turn_seq), UNIQUE(owner_id,conversation_id,generation,id,turn_seq))`,
		`CREATE TABLE IF NOT EXISTS mobile_messages(
 id TEXT PRIMARY KEY, owner_id TEXT NOT NULL, conversation_id TEXT NOT NULL, generation INTEGER NOT NULL,
 turn_id TEXT NOT NULL, turn_seq INTEGER NOT NULL, message_seq INTEGER NOT NULL CHECK(message_seq>0),
 role TEXT NOT NULL CHECK(role IN ('user','assistant')), text TEXT NOT NULL, created_at INTEGER NOT NULL,
 FOREIGN KEY(owner_id,conversation_id,generation,turn_id,turn_seq)
 REFERENCES mobile_transcript_turns(owner_id,conversation_id,generation,id,turn_seq) ON DELETE CASCADE,
 UNIQUE(owner_id,conversation_id,generation,message_seq), UNIQUE(owner_id,conversation_id,generation,turn_id,role))`,
	}
	for _, stmt := range stmts {
		if _, err = tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	var version int
	if checkErr1 := tx.QueryRowContext(ctx, `SELECT MAX(version) FROM mobile_chat_schema`).Scan(&version); checkErr1 != nil {
		return checkErr1
	}
	if version == 1 {
		for _, stmt := range turnSchema {
			if _, err = tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		version = 2
	}
	if version == 2 {
		for _, stmt := range runtimeSchema {
			if _, err = tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		version = 3
	}
	if version != 3 {
		return errors.New("unsupported mobile chat schema")
	}
	return tx.Commit()
}

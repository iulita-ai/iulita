package conversations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Errors distinguish invalid input, stale authority and unavailable resources.
var (
	// ErrIdempotency rejects reuse of an operation key for a different payload.
	ErrIdempotency = errors.New("mobile client message ID has a different payload")
	ErrQueueFull   = errors.New("mobile pending queue is full")
	ErrNoTurn      = errors.New("no runnable mobile turn")
)
var turnSchema = []string{
	`ALTER TABLE mobile_conversations ADD COLUMN next_turn_seq INTEGER NOT NULL DEFAULT 0 CHECK(next_turn_seq>=0)`,
	`UPDATE mobile_conversations SET next_turn_seq=COALESCE((SELECT MAX(turn_seq) FROM mobile_transcript_turns t WHERE t.owner_id=mobile_conversations.owner_id AND t.conversation_id=mobile_conversations.id),0)`,
	`CREATE TABLE mobile_turns(
 id TEXT PRIMARY KEY,owner_id TEXT NOT NULL,conversation_id TEXT NOT NULL,generation INTEGER NOT NULL,turn_seq INTEGER NOT NULL,
 client_message_id TEXT NOT NULL,digest BLOB NOT NULL CHECK(length(digest)=32),input_message_id TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('accepted','queued','running','awaiting_prompt','completed','failed','cancelled','unknown_outcome')),
 cancellation_requested INTEGER NOT NULL DEFAULT 0 CHECK(cancellation_requested IN (0,1)),
 profile_id TEXT NOT NULL,memory_mode TEXT NOT NULL CHECK(memory_mode IN ('isolated','account')),conversation_version INTEGER NOT NULL,
 admission_seq INTEGER NOT NULL,created_at INTEGER NOT NULL,
 FOREIGN KEY(owner_id,conversation_id,generation,id,turn_seq) REFERENCES mobile_transcript_turns(owner_id,conversation_id,generation,id,turn_seq) ON DELETE CASCADE,
 UNIQUE(owner_id,conversation_id,client_message_id),UNIQUE(owner_id,admission_seq))`,
	`CREATE INDEX mobile_turns_queue ON mobile_turns(owner_id,state,admission_seq)`,
	`CREATE TABLE mobile_event_sequences(owner_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,last_seq INTEGER NOT NULL CHECK(last_seq>0))`,
	`CREATE TABLE mobile_events(owner_id TEXT NOT NULL,event_seq INTEGER NOT NULL,id TEXT NOT NULL UNIQUE,conversation_id TEXT NOT NULL,generation INTEGER NOT NULL,turn_id TEXT NOT NULL,turn_seq INTEGER NOT NULL,input_message_id TEXT NOT NULL,event_type TEXT NOT NULL CHECK(event_type='turn.accepted'),created_at INTEGER NOT NULL,PRIMARY KEY(owner_id,event_seq),FOREIGN KEY(owner_id,conversation_id) REFERENCES mobile_conversations(owner_id,id) ON DELETE CASCADE)`,
	`ALTER TABLE mobile_chat_schema RENAME TO mobile_chat_schema_v1`,
	`CREATE TABLE mobile_chat_schema(version INTEGER PRIMARY KEY CHECK(version=2))`,
	`INSERT INTO mobile_chat_schema VALUES(2)`,
	`DROP TABLE mobile_chat_schema_v1`,
}

// Input contains a client message and its idempotency identity.
type Input struct {
	ClientMessageID string
	Generation      uint64
	Text            string
}

// Turn records the admission, execution and cancellation state of a message.
type Turn struct {
	ID, OwnerID, ConversationID, ClientMessageID, InputMessageID, State string
	Generation, TurnSeq, AdmissionSeq, ConversationVersion              uint64
	AcceptedSettings                                                    Settings
	CancellationRequested                                               bool
	CreatedAt                                                           time.Time
	CancellationRequestedAt                                             time.Time
	OutcomeReason, OutputMessageID                                      string
}

const turnColumns = `id,owner_id,conversation_id,client_message_id,input_message_id,state,generation,turn_seq,admission_seq,conversation_version,profile_id,memory_mode,cancellation_requested,created_at,cancelled_at,outcome_reason,COALESCE((SELECT m.id FROM mobile_messages m WHERE m.turn_id=mobile_turns.id AND m.owner_id=mobile_turns.owner_id AND m.role='assistant' LIMIT 1),'')`

func scanTurn(row *sql.Row) (Turn, error) {
	var t Turn
	var created int64
	var canceled int64
	err := row.Scan(&t.ID, &t.OwnerID, &t.ConversationID, &t.ClientMessageID, &t.InputMessageID, &t.State, &t.Generation, &t.TurnSeq, &t.AdmissionSeq, &t.ConversationVersion, &t.AcceptedSettings.ProfileID, &t.AcceptedSettings.MemoryMode, &t.CancellationRequested, &created, &canceled, &t.OutcomeReason, &t.OutputMessageID)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	t.CreatedAt = time.UnixMilli(created).UTC()
	if canceled > 0 {
		t.CancellationRequestedAt = time.UnixMilli(canceled).UTC()
	}
	return t, err
}
func payloadDigest(owner, id string, in Input) []byte {
	// Settings are snapshotted on FIRST admission. A later settings change must
	// not turn the same client-message retry into a new/conflicting request.
	b, _ := json.Marshal(struct { //nolint:errcheck // This internal snapshot contains only JSON-supported values.
		Version             int
		Owner, Conversation string
		Input               Input
	}{1, owner, id, in})
	digest := sha256.Sum256(b)
	return digest[:]
}

// Accept atomically commits the turn, bound input row, counters and private
// metadata outbox. No channel, model, tool or global bus is called here.
func (r *Repository) Accept(ctx context.Context, owner, id string, in Input) (Turn, bool, error) {
	if !validID(in.ClientMessageID) || in.Generation == 0 || !utf8.ValidString(in.Text) || in.Text == "" || len(in.Text) > 1<<20 {
		return Turn{}, false, ErrInvalid
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return Turn{}, false, err
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	c, err := get(ctx, tx, owner, id)
	if err != nil {
		return Turn{}, false, err
	}
	digest := payloadDigest(owner, id, in)
	existing, err := scanTurn(tx.QueryRowContext(ctx, `SELECT `+turnColumns+` FROM mobile_turns WHERE owner_id=? AND conversation_id=? AND client_message_id=?`, owner, id, in.ClientMessageID))
	if err == nil {
		var stored []byte
		if checkErr1 := tx.QueryRowContext(ctx, `SELECT digest FROM mobile_turns WHERE owner_id=? AND conversation_id=? AND client_message_id=?`, owner, id, in.ClientMessageID).Scan(&stored); checkErr1 != nil {
			return Turn{}, false, checkErr1
		}
		if !bytes.Equal(stored, digest) {
			return Turn{}, false, ErrIdempotency
		}
		return existing, true, tx.Commit()
	}
	if !errors.Is(err, ErrNotFound) {
		return Turn{}, false, err
	}
	// Duplicate lookup precedes archived/generation/quota checks by design.
	if c.Generation != in.Generation {
		return Turn{}, false, ErrConflict
	}
	if c.Archived {
		return Turn{}, false, ErrArchived
	}
	account, err := accountStamp(ctx, tx, owner)
	if err != nil {
		return Turn{}, false, err
	}
	var pending int
	if checkErr2 := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mobile_turns WHERE owner_id=? AND state IN ('accepted','queued')`, owner).Scan(&pending); checkErr2 != nil {
		return Turn{}, false, checkErr2
	}
	if pending >= 5 {
		return Turn{}, false, ErrQueueFull
	}
	var cachedBytes int64
	if checkErr3 := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(CAST(text AS BLOB))),0) FROM mobile_messages WHERE owner_id=?`, owner).Scan(&cachedBytes); checkErr3 != nil {
		return Turn{}, false, checkErr3
	}
	if cachedBytes+int64(len(in.Text)) > 64<<20 {
		return Turn{}, false, ErrQueueFull
	}
	var seq, admission uint64
	if checkErr4 := tx.QueryRowContext(ctx, `UPDATE mobile_conversations SET next_turn_seq=next_turn_seq+1,message_seq=message_seq+1,updated_at=? WHERE owner_id=? AND id=? AND generation=? RETURNING next_turn_seq`, time.Now().UnixMilli(), owner, id, in.Generation).Scan(&seq); checkErr4 != nil {
		return Turn{}, false, checkErr4
	}
	if checkErr5 := tx.QueryRowContext(ctx, `INSERT INTO mobile_event_sequences(owner_id,last_seq) VALUES(?,1) ON CONFLICT(owner_id) DO UPDATE SET last_seq=last_seq+1 RETURNING last_seq`, owner).Scan(&admission); checkErr5 != nil {
		return Turn{}, false, checkErr5
	}
	t := Turn{ID: uuid.NewString(), OwnerID: owner, ConversationID: id, ClientMessageID: in.ClientMessageID, InputMessageID: uuid.NewString(), State: "queued", Generation: in.Generation, TurnSeq: seq, AdmissionSeq: admission, ConversationVersion: c.Version, AcceptedSettings: c.Settings, CreatedAt: time.Now().UTC()}
	if _, err = tx.ExecContext(ctx, `INSERT INTO mobile_transcript_turns(id,owner_id,conversation_id,generation,turn_seq) VALUES(?,?,?,?,?)`, t.ID, owner, id, t.Generation, t.TurnSeq); err != nil {
		return Turn{}, false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO mobile_turns(id,owner_id,conversation_id,generation,turn_seq,client_message_id,digest,input_message_id,state,profile_id,memory_mode,conversation_version,admission_seq,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, t.ID, owner, id, t.Generation, t.TurnSeq, t.ClientMessageID, digest, t.InputMessageID, t.State, c.Settings.ProfileID, c.Settings.MemoryMode, c.Version, admission, t.CreatedAt.UnixMilli()); err != nil {
		return Turn{}, false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE mobile_turns SET account_stamp=? WHERE id=? AND owner_id=?`, account, t.ID, owner); err != nil {
		return Turn{}, false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO mobile_messages(id,owner_id,conversation_id,generation,turn_id,turn_seq,message_seq,role,text,created_at) VALUES(?,?,?,?,?,?,?,'user',?,?)`, t.InputMessageID, owner, id, t.Generation, t.ID, t.TurnSeq, c.MessageSeq+1, in.Text, t.CreatedAt.UnixMilli()); err != nil {
		return Turn{}, false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO mobile_events(owner_id,event_seq,id,conversation_id,generation,turn_id,turn_seq,input_message_id,event_type,created_at) VALUES(?,?,?,?,?,?,?,?,'turn.accepted',?)`, owner, admission, uuid.NewString(), id, t.Generation, t.ID, t.TurnSeq, t.InputMessageID, t.CreatedAt.UnixMilli()); err != nil {
		return Turn{}, false, err
	}
	if checkErr6 := bump(ctx, tx, owner); checkErr6 != nil {
		return Turn{}, false, checkErr6
	}
	return t, false, tx.Commit()
}

// GetTurn returns an owned turn by its server identifier.
func (r *Repository) GetTurn(ctx context.Context, owner, id, turnID string) (Turn, error) {
	return r.findTurn(ctx, owner, id, "id", turnID)
}

// GetTurnByClientID returns an owned turn by its client message identifier.
func (r *Repository) GetTurnByClientID(ctx context.Context, owner, id, clientID string) (Turn, error) {
	return r.findTurn(ctx, owner, id, "client_message_id", clientID)
}
func (r *Repository) findTurn(ctx context.Context, owner, id, column, key string) (Turn, error) {
	if !validID(key) {
		return Turn{}, ErrNotFound
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return Turn{}, err
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	if _, err = get(ctx, tx, owner, id); err != nil {
		return Turn{}, err
	}
	t, err := scanTurn(tx.QueryRowContext(ctx, `SELECT `+turnColumns+` FROM mobile_turns WHERE owner_id=? AND conversation_id=? AND `+column+`=?`, owner, id, key))
	if err != nil {
		return t, err
	}
	return t, tx.Commit()
}

// ClaimNext is only a worker admission primitive. BE06/BE09 still own execution,
// cancellation, processing epochs and terminal commits. No runner starts here.
func (r *Repository) ClaimNext(ctx context.Context, owner string) (Turn, error) {
	if !validID(owner) {
		return Turn{}, ErrNotFound
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return Turn{}, err
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	var active int
	if checkErr7 := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mobile_turns WHERE owner_id=? AND state IN ('running','awaiting_prompt')`, owner).Scan(&active); checkErr7 != nil {
		return Turn{}, checkErr7
	}
	if active >= 2 {
		return Turn{}, ErrNoTurn
	}
	var id, conversation string
	err = tx.QueryRowContext(ctx, `SELECT t.id,t.conversation_id FROM mobile_turns t JOIN mobile_conversations c ON c.id=t.conversation_id AND c.owner_id=t.owner_id JOIN users u ON u.id=t.owner_id WHERE t.owner_id=? AND t.state IN ('queued','accepted') AND t.generation=c.generation AND t.cancellation_requested=0 AND NOT EXISTS(SELECT 1 FROM mobile_turns p WHERE p.owner_id=t.owner_id AND p.conversation_id=t.conversation_id AND p.turn_seq<t.turn_seq AND p.state IN ('accepted','queued','running','awaiting_prompt')) ORDER BY t.admission_seq LIMIT 1`, owner).Scan(&id, &conversation)
	if errors.Is(err, sql.ErrNoRows) {
		return Turn{}, ErrNoTurn
	}
	if err != nil {
		return Turn{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE mobile_turns SET state='running' WHERE owner_id=? AND conversation_id=? AND id=? AND state IN ('accepted','queued')`, owner, conversation, id); err != nil {
		return Turn{}, err
	}
	t, err := scanTurn(tx.QueryRowContext(ctx, `SELECT `+turnColumns+` FROM mobile_turns WHERE owner_id=? AND conversation_id=? AND id=?`, owner, conversation, id))
	if err != nil {
		return t, err
	}
	return t, tx.Commit()
}

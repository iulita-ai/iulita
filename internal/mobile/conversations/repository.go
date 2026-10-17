// Package conversations implements owned mobile chat persistence. owner is a
// trusted, authenticated account ID supplied by the service, never request data.
package conversations

import (
	"context"
	"crypto/cipher"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/iulita-ai/iulita/internal/mobile/securetoken"
	"github.com/iulita-ai/iulita/internal/mobile/sqlitedb"
)

// Errors distinguish invalid input, stale authority and unavailable resources.
var (
	// ErrNotFound indicates that an owned resource is unavailable.
	ErrNotFound = errors.New("mobile conversation not found")
	ErrConflict = errors.New("stale mobile conversation version or generation")
	ErrArchived = errors.New("mobile conversation is archived")
	ErrInvalid  = errors.New("invalid mobile conversation input")
)

// Settings contains persistent-store or runtime configuration.
type Settings struct {
	ProfileID  string
	MemoryMode string
}

// Conversation records an owned chat and its current settings.
type Conversation struct {
	ID, OwnerID, Title               string
	Settings                         Settings
	Version, Generation              uint64
	Archived                         bool
	MessageSeq, ReadSeq, UnreadCount uint64
	CreatedAt, UpdatedAt             time.Time
}

// Message records one message in an owned conversation.
type Message struct {
	ID, ConversationID, TurnID, Role, Text string
	TurnSeq, MessageSeq, Generation        uint64
	CreatedAt                              time.Time
}

// Repository stores owned conversations, turns and durable operation metadata.
type Repository struct {
	media          cipher.AEAD
	db             *sql.DB
	authorityReads *sql.DB
	cursors        *securetoken.Signer
}

// OpenExisting opens an initialized conversation store without bootstrapping it.
func OpenExisting(ctx context.Context, path string, cursors *securetoken.Signer) (*Repository, error) {
	if cursors == nil {
		return nil, ErrInvalid
	}
	db, err := sqlitedb.Open(ctx, path, false)
	if err != nil {
		return nil, err
	}
	if err = migrate(ctx, db); err != nil {
		db.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
		return nil, err
	}
	reads, err := sqlitedb.OpenReadOnly(ctx, path)
	if err != nil {
		db.Close() //nolint:errcheck,gosec // Preserve the reader initialization error.
		return nil, err
	}
	return &Repository{db: db, authorityReads: reads, cursors: cursors}, nil
}

// Close releases the resources owned by this instance.
func (r *Repository) Close() error { return errors.Join(r.authorityReads.Close(), r.db.Close()) }
func validID(s string) bool {
	u, err := uuid.Parse(s)
	return err == nil && u.String() == s && u != uuid.Nil
}
func validSettings(s Settings) bool {
	return s.ProfileID != "" && len(s.ProfileID) <= 128 && (s.MemoryMode == "isolated" || s.MemoryMode == "account")
}
func validTitle(s string) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) <= 200 && strings.TrimSpace(s) != ""
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

const columns = `id,owner_id,title,profile_id,memory_mode,version,generation,archived,message_seq,read_seq,created_at,updated_at`

func get(ctx context.Context, q queryer, owner, id string) (Conversation, error) {
	var c Conversation
	var created, updated int64
	if !validID(owner) || !validID(id) {
		return c, ErrNotFound
	}
	err := q.QueryRowContext(ctx, `SELECT `+columns+` FROM mobile_conversations WHERE owner_id=? AND id=? AND owner_id IN (SELECT id FROM users)`, owner, id).Scan(&c.ID, &c.OwnerID, &c.Title, &c.Settings.ProfileID, &c.Settings.MemoryMode, &c.Version, &c.Generation, &c.Archived, &c.MessageSeq, &c.ReadSeq, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	c.CreatedAt = time.UnixMilli(created).UTC()
	c.UpdatedAt = time.UnixMilli(updated).UTC()
	return c, err
}
func getFull(ctx context.Context, q queryer, owner, id string) (Conversation, error) {
	c, err := get(ctx, q, owner, id)
	if err == nil {
		err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM mobile_messages WHERE owner_id=? AND conversation_id=? AND generation=? AND message_seq>? AND role='assistant'`, owner, id, c.Generation, c.ReadSeq).Scan(&c.UnreadCount)
	}
	return c, err
}

// Get returns the owned resource under its current access constraints.
func (r *Repository) Get(ctx context.Context, owner, id string) (Conversation, error) {
	tx, err := r.begin(ctx)
	if err != nil {
		return Conversation{}, err
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	c, err := getFull(ctx, tx, owner, id)
	if err != nil {
		return c, err
	}
	return c, tx.Commit()
}
func bump(ctx context.Context, tx *repoTx, owner string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO mobile_conversation_catalog(owner_id,revision) VALUES(?,1) ON CONFLICT(owner_id) DO UPDATE SET revision=revision+1`, owner)
	return err
}

// Create atomically creates the owned resource after validating authority.
func (r *Repository) Create(ctx context.Context, owner, title string, settings Settings) (Conversation, error) {
	if !validID(owner) || !validTitle(title) || !validSettings(settings) {
		return Conversation{}, ErrInvalid
	}
	id := uuid.NewString()
	now := time.Now().UTC().UnixMilli()
	tx, err := r.begin(ctx)
	if err != nil {
		return Conversation{}, err
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	var count int
	if checkErr1 := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mobile_conversations WHERE owner_id=?`, owner).Scan(&count); checkErr1 != nil {
		return Conversation{}, checkErr1
	}
	if count >= 500 {
		return Conversation{}, ErrQueueFull
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO mobile_conversations(id,owner_id,title,profile_id,memory_mode,created_at,updated_at) SELECT ?,?,?,?,?,?,? WHERE EXISTS(SELECT 1 FROM users WHERE id=?)`, id, owner, title, settings.ProfileID, settings.MemoryMode, now, now, owner)
	if err != nil {
		return Conversation{}, err
	}
	n, operationErr1 := res.RowsAffected()
	if operationErr1 != nil {
		return Conversation{}, operationErr1
	}
	if n != 1 {
		return Conversation{}, ErrNotFound
	}
	if checkErr2 := bump(ctx, tx, owner); checkErr2 != nil {
		return Conversation{}, checkErr2
	}
	c, err := getFull(ctx, tx, owner, id)
	if err != nil {
		return c, err
	}
	if checkErr3 := r.event(ctx, tx, owner, id, c.Generation, "", 0, "", "conversation.changed"); checkErr3 != nil {
		return c, checkErr3
	}
	return c, tx.Commit()
}
func (r *Repository) mutate(ctx context.Context, owner, id string, version, generation uint64, fn func(*repoTx, Conversation) error) (Conversation, error) {
	tx, err := r.begin(ctx)
	if err != nil {
		return Conversation{}, err
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	c, err := get(ctx, tx, owner, id)
	if err != nil {
		return c, err
	}
	if (version != 0 && c.Version != version) || (generation != 0 && c.Generation != generation) {
		return Conversation{}, ErrConflict
	}
	if checkErr4 := fn(tx, c); checkErr4 != nil {
		return Conversation{}, checkErr4
	}
	if checkErr5 := bump(ctx, tx, owner); checkErr5 != nil {
		return Conversation{}, checkErr5
	}
	oldGeneration := c.Generation
	c, err = getFull(ctx, tx, owner, id)
	if err != nil {
		return c, err
	}
	kind := "conversation.changed"
	if c.Generation != oldGeneration {
		kind = "generation.reset"
	}
	if checkErr6 := r.event(ctx, tx, owner, id, c.Generation, "", 0, "", kind); checkErr6 != nil {
		return c, checkErr6
	}
	return c, tx.Commit()
}

// Update updates owned conversation settings with version checks.
func (r *Repository) Update(ctx context.Context, owner, id string, version uint64, title *string, settings *Settings) (Conversation, error) {
	if version == 0 || (title == nil && settings == nil) || (title != nil && !validTitle(*title)) || (settings != nil && !validSettings(*settings)) {
		return Conversation{}, ErrInvalid
	}
	return r.mutate(ctx, owner, id, version, 0, func(tx *repoTx, c Conversation) error {
		if title != nil {
			c.Title = *title
		}
		if settings != nil {
			c.Settings = *settings
		}
		_, err := tx.ExecContext(ctx, `UPDATE mobile_conversations SET title=?,profile_id=?,memory_mode=?,version=version+1,updated_at=? WHERE owner_id=? AND id=? AND version=?`, c.Title, c.Settings.ProfileID, c.Settings.MemoryMode, time.Now().UnixMilli(), owner, id, version)
		return err
	})
}

// Archive archives an owned conversation with version checks.
func (r *Repository) Archive(ctx context.Context, owner, id string, version uint64, archived bool) (Conversation, error) {
	if version == 0 {
		return Conversation{}, ErrInvalid
	}
	return r.mutate(ctx, owner, id, version, 0, func(tx *repoTx, c Conversation) error {
		_, err := tx.ExecContext(ctx, `UPDATE mobile_conversations SET archived=?,version=version+1,updated_at=? WHERE owner_id=? AND id=? AND version=?`, archived, time.Now().UnixMilli(), owner, id, version)
		return err
	})
}

// Clear clears owned history and invalidates its prior execution generation.
func (r *Repository) Clear(ctx context.Context, owner, id string, generation uint64) (Conversation, error) {
	if generation == 0 {
		return Conversation{}, ErrInvalid
	}
	return r.mutate(ctx, owner, id, 0, generation, func(tx *repoTx, c Conversation) error {
		if _, err := tx.ExecContext(ctx, `UPDATE mobile_prompts SET state='invalidated' WHERE owner_id=? AND conversation_id=? AND state='open'`, owner, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM mobile_snapshots WHERE owner_id=?`, owner); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM mobile_chat_photos WHERE owner_id=? AND conversation_id=?`, owner, id); err != nil {
			return err
		}
		// Remove text atomically, retaining turn ID tombstones for BE05/BE06.
		// Old turn IDs cannot be rebound to the new generation.
		if _, err := tx.ExecContext(ctx, `UPDATE mobile_turns SET cancellation_requested=1,processing_epoch=processing_epoch+1,cancelled_at=strftime('%s','now')*1000,state=CASE WHEN EXISTS(SELECT 1 FROM mobile_tool_operations o WHERE o.owner_id=mobile_turns.owner_id AND o.turn_id=mobile_turns.id AND (o.state IN ('started','unknown_outcome') OR (o.state='succeeded' AND o.effect<>'read'))) THEN 'unknown_outcome' ELSE 'cancelled' END WHERE owner_id=? AND conversation_id=? AND generation=? AND state IN ('accepted','queued','running','awaiting_prompt')`, owner, id, generation); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM mobile_messages WHERE owner_id=? AND conversation_id=?`, owner, id); err != nil {
			return err
		}
		// Keep dedup/effect tombstones, but erase copied knowledge and prompts.
		for _, q := range []string{
			`UPDATE mobile_turns SET policy_snapshot='{}' WHERE owner_id=? AND conversation_id=?`,
			`UPDATE mobile_reminders SET state='cancelled',title='',authority='{}' WHERE owner_id=? AND conversation_id=?`,
			`UPDATE mobile_prompts SET body='{}',nonce_hash=NULL,fingerprint=NULL,decision='',actor_device='' WHERE owner_id=? AND conversation_id=?`,
			`UPDATE mobile_tool_operations SET outcome='' WHERE owner_id=? AND conversation_id=?`,
			`UPDATE mobile_operations SET response=X'',redacted=1 WHERE owner_id=? AND conversation_id=?`,
		} {
			if _, err := tx.ExecContext(ctx, q, owner, id); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE mobile_conversations SET generation=generation+1,version=version+1,message_seq=0,read_seq=0,updated_at=? WHERE owner_id=? AND id=? AND generation=?`, time.Now().UnixMilli(), owner, id, generation)
		return err
	})
}

// BindTurn is a persistence primitive for BE05, not admission/SendMessage.
func (r *Repository) BindTurn(ctx context.Context, owner, id string, generation uint64, turnID string, seq uint64) error {
	if !validID(turnID) || generation == 0 || seq == 0 || seq > 1<<62 {
		return ErrInvalid
	}
	_, err := r.mutate(ctx, owner, id, 0, generation, func(tx *repoTx, c Conversation) error {
		if c.Archived {
			return ErrArchived
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO mobile_transcript_turns(id,owner_id,conversation_id,generation,turn_seq) VALUES(?,?,?,?,?)`, turnID, owner, id, generation, seq)
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE mobile_conversations SET next_turn_seq=MAX(next_turn_seq,?) WHERE owner_id=? AND id=?`, seq, owner, id)
		}
		return err
	})
	return err
}

// Append appends a message to the owned conversation.
func (r *Repository) Append(ctx context.Context, owner, id string, generation uint64, turnID string, turnSeq uint64, role, text string) (Message, error) {
	if !validID(turnID) || generation == 0 || turnSeq == 0 || (role != "user" && role != "assistant") || !utf8.ValidString(text) || len(text) > 1<<20 {
		return Message{}, ErrInvalid
	}
	var m Message
	_, err := r.mutate(ctx, owner, id, 0, generation, func(tx *repoTx, c Conversation) error {
		if role == "user" && c.Archived {
			return ErrArchived
		}
		var bound int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mobile_transcript_turns WHERE owner_id=? AND conversation_id=? AND generation=? AND id=? AND turn_seq=?`, owner, id, generation, turnID, turnSeq).Scan(&bound); err != nil {
			return err
		}
		if bound != 1 {
			return ErrNotFound
		}
		m = Message{ID: uuid.NewString(), ConversationID: id, TurnID: turnID, Role: role, Text: text, TurnSeq: turnSeq, MessageSeq: c.MessageSeq + 1, Generation: generation, CreatedAt: time.Now().UTC()}
		if _, err := tx.ExecContext(ctx, `INSERT INTO mobile_messages(id,owner_id,conversation_id,generation,turn_id,turn_seq,message_seq,role,text,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, m.ID, owner, id, generation, turnID, turnSeq, m.MessageSeq, role, text, m.CreatedAt.UnixMilli()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE mobile_conversations SET message_seq=?,updated_at=? WHERE owner_id=? AND id=? AND generation=?`, m.MessageSeq, m.CreatedAt.UnixMilli(), owner, id, generation)
		return err
	})
	return m, err
}

// MarkRead advances the owned conversation read position.
func (r *Repository) MarkRead(ctx context.Context, owner, id string, generation, seq uint64) (Conversation, error) {
	if generation == 0 {
		return Conversation{}, ErrInvalid
	}
	return r.mutate(ctx, owner, id, 0, generation, func(tx *repoTx, c Conversation) error {
		if seq > c.MessageSeq {
			return ErrInvalid
		}
		if seq > 0 {
			var exists int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mobile_messages WHERE owner_id=? AND conversation_id=? AND generation=? AND message_seq=?`, owner, id, generation, seq).Scan(&exists); err != nil {
				return err
			}
			if exists != 1 {
				return ErrInvalid
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE mobile_conversations SET read_seq=MAX(read_seq,?) WHERE owner_id=? AND id=? AND generation=?`, seq, owner, id, generation)
		return err
	})
}

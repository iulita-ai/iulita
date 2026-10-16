package conversations

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/iulita-ai/iulita/internal/mobile/sqlitedb"
)

// MaxPhotoBytes bounds canonical JPEG input per image.
const MaxPhotoBytes = 384 << 10

// MaxPhotos bounds attachments per chat message.
const MaxPhotos = 4
const mediaKeyName = "mobile-chat-media-v1.key"

var photoDecoders = make(chan struct{}, 2)

// Photo is a canonical gallery image. Raw bytes never enter policy or journals.
type Photo struct {
	Digest        string
	Width, Height int
	Data          []byte `json:"-"`
}

// PhotoInfo describes an owned image without exposing its bytes.
type PhotoInfo struct {
	ID, MessageID                 string
	Position, Width, Height, Size int
}

// CanonicalPhotos bounds decoding and discards metadata, including EXIF/GPS.
func CanonicalPhotos(ctx context.Context, inputs [][]byte) ([]Photo, error) {
	if len(inputs) == 0 || len(inputs) > MaxPhotos {
		return nil, ErrInvalid
	}
	select {
	case photoDecoders <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-photoDecoders }()
	out := make([]Photo, 0, len(inputs))
	for _, data := range inputs {
		if len(data) == 0 || len(data) > MaxPhotoBytes {
			return nil, ErrInvalid
		}
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
		if err != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 2048 || cfg.Height > 2048 {
			return nil, ErrInvalid
		}
		im, err := jpeg.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, ErrInvalid
		}
		var b bytes.Buffer
		// Re-encoding drops every metadata segment, including inter-scan data.
		// Adapt quality so a valid client-compressed image is not needlessly
		// rejected merely because quality85 expands its encoded size.
		for quality := 85; quality >= 5; quality -= 10 {
			b.Reset()
			if err = jpeg.Encode(&b, im, &jpeg.Options{Quality: quality}); err != nil {
				return nil, ErrInvalid
			}
			if b.Len() <= MaxPhotoBytes {
				break
			}
		}
		if b.Len() > MaxPhotoBytes {
			return nil, ErrInvalid
		}
		sum := sha256.Sum256(b.Bytes())
		out = append(out, Photo{hex.EncodeToString(sum[:]), cfg.Width, cfg.Height, b.Bytes()})
	}
	return out, nil
}

// InitializeMediaKey is explicit and refuses to replace a lost key for stored media.
func InitializeMediaKey(ctx context.Context, accountPath, dir string) error {
	if _, err := LoadMediaKey(dir); err == nil {
		return nil
	}
	path := filepath.Join(dir, mediaKeyName)
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		return errors.New("chat media key invalid")
	}
	db, err := sqlitedb.Open(ctx, accountPath, false)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck // Cleanup only; preserve the operation result.
	var table int
	if stepError1 := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='mobile_chat_photos'`).Scan(&table); stepError1 != nil {
		return stepError1
	}
	if table > 0 {
		var count int
		if stepError2 := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mobile_chat_photos`).Scan(&count); stepError2 != nil {
			return stepError2
		}
		if count > 0 {
			return errors.New("restore missing chat media key from backup")
		}
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("private media key directory required")
	}
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".chat-media-key-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) //nolint:errcheck // Remove only our private temporary key file.
	if err = f.Chmod(0o600); err == nil {
		_, err = f.Write(key)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Link(f.Name(), path); err != nil && !os.IsExist(err) {
		return err
	}
	d, err := os.Open(dir) //nolint:gosec // Explicit operator-controlled private realm directory.
	if err != nil {
		return err
	}
	defer d.Close() //nolint:errcheck // Cleanup only; preserve the operation result.
	if stepError3 := d.Sync(); stepError3 != nil {
		return stepError3
	}
	_, err = LoadMediaKey(dir)
	return err
}

// LoadMediaKey never generates or resets a key during normal startup.
func LoadMediaKey(dir string) ([]byte, error) {
	parent, err := os.Lstat(dir)
	if err != nil || !parent.IsDir() || parent.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("private media key directory required")
	}
	path := filepath.Join(dir, mediaKeyName)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() != 32 {
		return nil, errors.New("chat media key unavailable")
	}
	f, err := os.Open(path) //nolint:gosec // Fixed key filename under the validated private realm directory.
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck // Cleanup only; preserve the operation result.
	opened, statErr := f.Stat()
	if statErr != nil || !os.SameFile(info, opened) {
		return nil, errors.New("chat media key changed during read")
	}
	key, err := io.ReadAll(io.LimitReader(f, 33))
	if err != nil || len(key) != 32 {
		return nil, errors.New("chat media key unavailable")
	}
	return key, nil
}

// ConfigureMedia installs the immutable dedicated key before serving requests.
func (r *Repository) ConfigureMedia(key []byte) error {
	if len(key) != 32 {
		return ErrInvalid
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // Startup cleanup only.
	sum := sha256.Sum256(key)
	var old []byte
	err = tx.QueryRow(`SELECT fingerprint FROM mobile_chat_media_key WHERE slot=1`).Scan(&old)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		var count int
		if e := tx.QueryRow(`SELECT COUNT(*) FROM mobile_chat_photos`).Scan(&count); e != nil {
			return e
		}
		if count > 0 {
			return ErrInvalid
		}
		if _, e := tx.Exec(`INSERT INTO mobile_chat_media_key(slot,fingerprint) VALUES(1,?)`, sum[:]); e != nil {
			return e
		}
	case err != nil:
		return err
	case !bytes.Equal(old, sum[:]):
		return errors.New("chat media key does not match stored data")
	}
	if e := tx.Commit(); e != nil {
		return e
	}
	r.media = aead
	return nil
}
func mediaAAD(owner, conversation, message, id string, generation uint64, position int) []byte {
	b, _ := json.Marshal([]any{"iulita.chat.photo.v1", owner, conversation, generation, message, id, position}) //nolint:errcheck // Only JSON-supported scalar values.
	return b
}
func (r *Repository) storePhotos(ctx context.Context, tx *repoTx, t Turn, photos []Photo) error {
	if len(photos) == 0 {
		return nil
	}
	if r.media == nil || len(photos) > MaxPhotos {
		return ErrInvalid
	}
	var total int
	for _, p := range photos {
		sum := sha256.Sum256(p.Data)
		if len(p.Data) == 0 || len(p.Data) > MaxPhotoBytes || p.Digest != hex.EncodeToString(sum[:]) || p.Width < 1 || p.Height < 1 || p.Width > 2048 || p.Height > 2048 {
			return ErrInvalid
		}
		total += len(p.Data) + r.media.NonceSize() + r.media.Overhead()
	}
	var owned, global int64
	if stepError4 := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(ciphertext)),0),COALESCE(SUM(CASE WHEN owner_id=? THEN length(ciphertext) ELSE 0 END),0) FROM mobile_chat_photos`, t.OwnerID).Scan(&global, &owned); stepError4 != nil {
		return stepError4
	}
	if owned+int64(total) > 128<<20 || global+int64(total) > 512<<20 {
		return ErrQueueFull
	}
	for pos, p := range photos {
		id := uuid.NewString()
		nonce := make([]byte, r.media.NonceSize())
		if _, err := rand.Read(nonce); err != nil {
			return err
		}
		data := r.media.Seal(nonce, nonce, p.Data, mediaAAD(t.OwnerID, t.ConversationID, t.InputMessageID, id, t.Generation, pos))
		if _, err := tx.ExecContext(ctx, `INSERT INTO mobile_chat_photos(id,owner_id,conversation_id,generation,message_id,position,width,height,size,ciphertext) VALUES(?,?,?,?,?,?,?,?,?,?)`, id, t.OwnerID, t.ConversationID, t.Generation, t.InputMessageID, pos, p.Width, p.Height, len(p.Data), data); err != nil {
			return err
		}
	}
	return nil
}

// ListPhotos returns metadata for bounded message IDs in the current history.
func (r *Repository) ListPhotos(ctx context.Context, owner, conversation string, generation uint64, messages []string) ([]PhotoInfo, error) {
	if len(messages) > 100 {
		return nil, ErrInvalid
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	c, err := get(ctx, tx, owner, conversation)
	if err != nil {
		return nil, err
	}
	if c.Generation != generation {
		return nil, ErrConflict
	}
	out := []PhotoInfo{}
	for _, m := range messages {
		if !validID(m) {
			return nil, ErrInvalid
		}
		queryErr := func() error {
			rows, e := tx.QueryContext(ctx, `SELECT id,message_id,position,width,height,size FROM mobile_chat_photos WHERE owner_id=? AND conversation_id=? AND generation=? AND message_id=? ORDER BY position`, owner, conversation, generation, m)
			if e != nil {
				return e
			}
			defer rows.Close() //nolint:errcheck // Read cleanup only.
			for rows.Next() {
				var p PhotoInfo
				if scanError := rows.Scan(&p.ID, &p.MessageID, &p.Position, &p.Width, &p.Height, &p.Size); scanError != nil {
					return scanError
				}
				out = append(out, p)
			}
			return rows.Err()
		}()
		if queryErr != nil {
			return nil, queryErr
		}
	}
	return out, tx.Commit()
}

// GetPhoto decrypts an image only after checking current ownership and generation.
func (r *Repository) GetPhoto(ctx context.Context, owner, conversation string, generation uint64, id string) (PhotoInfo, []byte, error) {
	tx, err := r.begin(ctx)
	if err != nil {
		return PhotoInfo{}, nil, err
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	c, err := get(ctx, tx, owner, conversation)
	if err != nil {
		return PhotoInfo{}, nil, err
	}
	if c.Generation != generation {
		return PhotoInfo{}, nil, ErrConflict
	}
	p, data, err := r.photo(ctx, tx, owner, conversation, generation, id)
	if err != nil {
		return p, nil, err
	}
	return p, data, tx.Commit()
}
func (r *Repository) photo(ctx context.Context, tx *repoTx, owner, conversation string, generation uint64, id string) (PhotoInfo, []byte, error) {
	var p PhotoInfo
	var body []byte
	err := tx.QueryRowContext(ctx, `SELECT id,message_id,position,width,height,size,ciphertext FROM mobile_chat_photos WHERE owner_id=? AND conversation_id=? AND generation=? AND id=?`, owner, conversation, generation, id).Scan(&p.ID, &p.MessageID, &p.Position, &p.Width, &p.Height, &p.Size, &body)
	if err != nil {
		return p, nil, ErrNotFound
	}
	if r.media == nil || len(body) < r.media.NonceSize() {
		return p, nil, ErrInvalid
	}
	data, err := r.media.Open(nil, body[:r.media.NonceSize()], body[r.media.NonceSize():], mediaAAD(owner, conversation, p.MessageID, id, generation, p.Position))
	if err != nil {
		return p, nil, ErrInvalid
	}
	return p, data, nil
}

// TurnPhotos reads only the current input under the running turn fence.
func (r *Repository) TurnPhotos(ctx context.Context, f Fence) ([][]byte, error) {
	tx, err := r.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	if stepError5 := r.fence(ctx, tx, f); stepError5 != nil {
		return nil, stepError5
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM mobile_chat_photos WHERE owner_id=? AND conversation_id=? AND generation=? AND message_id=(SELECT input_message_id FROM mobile_turns WHERE owner_id=? AND conversation_id=? AND id=?) ORDER BY position`, f.OwnerID, f.ConversationID, f.Generation, f.OwnerID, f.ConversationID, f.TurnID)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	defer rows.Close() //nolint:errcheck // Fallback cleanup; explicit close below precedes the next query.
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
	if err != nil {
		return nil, err
	}
	out := [][]byte{}
	for _, id := range ids {
		_, data, e := r.photo(ctx, tx, f.OwnerID, f.ConversationID, f.Generation, id)
		if e != nil {
			return nil, e
		}
		out = append(out, data)
	}
	return out, tx.Commit()
}

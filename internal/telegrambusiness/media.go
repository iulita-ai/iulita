package telegrambusiness

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gofrs/flock"
)

// MaxMediaBytes is the strict owner-upload byte limit before encryption.
const MaxMediaBytes = 1 << 20

var mediaRefPattern = regexp.MustCompile(`^bmedia_[0-9a-f]{48}$`)

// MediaAuthority binds an upload to one persisted consent and lifecycle snapshot.
type MediaAuthority struct {
	Epoch              uint64 `json:"epoch"`
	PeerRevision       uint64 `json:"peer_revision"`
	OwnerSecurityEpoch uint64 `json:"owner_security_epoch"`
	RuntimeGeneration  string `json:"runtime_generation"`
	RestoreGeneration  string `json:"restore_generation"`
}

// MediaAuthorityFor captures server-owned authority, never client JSON claims.
func MediaAuthorityFor(c Connection, p *Peer) MediaAuthority {
	a := MediaAuthority{Epoch: c.Epoch, OwnerSecurityEpoch: c.OwnerSecurityEpoch, RuntimeGeneration: c.RuntimeGeneration, RestoreGeneration: c.RestoreGeneration}
	if p != nil {
		a.PeerRevision = p.Revision
	}
	return a
}
func mediaScopePrefix(scope Scope) string {
	data, err := json.Marshal(struct {
		Key   Key
		Owner string
	}{scope.Key, scope.OwnerID})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return "bmedia_" + hex.EncodeToString(sum[:8])
}

// MediaCrypto requires an enabled authenticated local encryption provider.
type MediaCrypto interface {
	EncryptionEnabled() bool
	Encrypt(string) (string, error)
	Decrypt(string) (string, error)
}

// MediaBlob is encrypted at rest and materialized only for its exact scope.
type MediaBlob struct {
	Ref       string         `json:"ref"`
	Scope     Scope          `json:"scope"`
	Filename  string         `json:"filename"`
	MIME      string         `json:"mime_type"`
	Data      []byte         `json:"data"`
	ExpiresAt time.Time      `json:"expires_at"`
	Authority MediaAuthority `json:"authority"`
}

// MediaVault holds short-lived owner-uploaded bytes only as authenticated
// ciphertext. Local uploads perform no Telegram API or provider operation.
type MediaVault struct {
	root   string
	files  *os.Root
	crypto MediaCrypto
	now    func() time.Time
}

// NewMediaVault pins a protected directory descriptor; later file operations
// cannot escape it through a renamed parent or a replaced path component.
func NewMediaVault(root string, crypto MediaCrypto) (*MediaVault, error) {
	if crypto == nil || !crypto.EncryptionEnabled() {
		return nil, ErrUnavailable
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, ErrUnavailable
	}
	if err = os.MkdirAll(root, 0o700); err != nil {
		return nil, ErrUnavailable
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrDenied
	}
	files, err := os.OpenRoot(root)
	if err != nil {
		return nil, ErrUnavailable
	}
	fail := func(cause error) (*MediaVault, error) {
		if closeErr := files.Close(); closeErr != nil {
			return nil, ErrUnavailable
		}
		return nil, cause
	}
	opened, err := files.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return fail(ErrDenied)
	}
	dir, err := files.Open(".")
	if err != nil {
		return fail(ErrUnavailable)
	}
	chmodErr := dir.Chmod(0o700)
	closeErr := dir.Close()
	if chmodErr != nil || closeErr != nil {
		return fail(ErrUnavailable)
	}
	lock, err := files.OpenFile(".quota.lock", os.O_CREATE|os.O_EXCL|os.O_RDONLY, 0o600)
	if err == nil {
		if closeErr = lock.Close(); closeErr != nil {
			return fail(ErrUnavailable)
		}
	} else if !os.IsExist(err) {
		return fail(ErrUnavailable)
	}
	lockInfo, err := files.Lstat(".quota.lock")
	if err != nil || !lockInfo.Mode().IsRegular() || lockInfo.Mode().Perm()&0o077 != 0 {
		return fail(ErrDenied)
	}
	return &MediaVault{root: root, files: files, crypto: crypto, now: time.Now}, nil
}

// Close releases the pinned directory descriptor after all users have stopped.
func (v *MediaVault) Close() error { return v.files.Close() }

func (v *MediaVault) quotaLock(ctx context.Context) (*flock.Flock, error) {
	// flock opens a path, so reject directory replacement and a non-regular
	// lock before and after acquisition. Content IO uses the pinned os.Root.
	before, err := v.files.Stat(".")
	current, pathErr := os.Lstat(v.root)
	lockInfo, lockErr := v.files.Lstat(".quota.lock")
	if err != nil || pathErr != nil || !current.IsDir() || !os.SameFile(before, current) || lockErr != nil || !lockInfo.Mode().IsRegular() || lockInfo.Mode().Perm()&0o077 != 0 {
		return nil, ErrDenied
	}
	lock := flock.New(filepath.Join(v.root, ".quota.lock"), flock.SetPermissions(0o600))
	locked, err := lock.TryLockContext(ctx, 25*time.Millisecond)
	if err != nil || !locked {
		return nil, ErrUnavailable
	}
	after, err := v.files.Lstat(".quota.lock")
	pathInfo, pathErr := os.Lstat(filepath.Join(v.root, ".quota.lock"))
	if err != nil || pathErr != nil || !after.Mode().IsRegular() || !os.SameFile(lockInfo, after) || !os.SameFile(after, pathInfo) {
		if unlockErr := lock.Unlock(); unlockErr != nil {
			return nil, ErrUnavailable
		}
		return nil, ErrDenied
	}
	return lock, nil
}

func (v *MediaVault) entries() ([]os.DirEntry, error) {
	dir, err := v.files.Open(".")
	if err != nil {
		return nil, ErrUnavailable
	}
	entries, readErr := dir.ReadDir(10001)
	closeErr := dir.Close()
	if (readErr != nil && !errors.Is(readErr, io.EOF)) || closeErr != nil {
		return nil, ErrUnavailable
	}
	if len(entries) > 10000 {
		return nil, ErrQuota
	}
	return entries, nil
}

func validMediaFile(filename, mime string, data []byte) bool {
	if filename == "" || filename == "." || filename == ".." || len(filename) > 128 || strings.ContainsAny(filename, "/\\") {
		return false
	}
	for _, r := range filename {
		if unicode.IsControl(r) {
			return false
		}
	}
	detected := http.DetectContentType(data)
	switch mime {
	case "image/jpeg", "image/png":
		if detected != mime {
			return false
		}
		config, _, err := image.DecodeConfig(bytes.NewReader(data))
		return err == nil && config.Width > 0 && config.Height > 0 && config.Width <= 16000000/config.Height
	case "video/mp4":
		return detected == "video/mp4"
	case "audio/ogg":
		return detected == "application/ogg"
	case "audio/mpeg":
		return detected == "audio/mpeg"
	case "application/pdf":
		return bytes.HasPrefix(data, []byte("%PDF-"))
	case "text/plain":
		return utf8.Valid(data) && strings.HasPrefix(detected, "text/plain") && !bytes.ContainsRune(data, 0)
	default:
		return false
	}
}

// Put writes only ciphertext, within connection/global quotas and a finite TTL.
func (v *MediaVault) Put(ctx context.Context, scope Scope, filename, mime string, data []byte, authority MediaAuthority) (ref string, err error) {
	if contextErr := ctx.Err(); contextErr != nil {
		return "", contextErr
	}
	if scope.validate() != nil || len(data) == 0 || len(data) > MaxMediaBytes {
		return "", ErrQuota
	}
	if authority.Epoch == 0 || authority.OwnerSecurityEpoch == 0 || authority.RuntimeGeneration == "" || !validMediaFile(filename, mime, data) {
		return "", ErrDenied
	}
	lock, err := v.quotaLock(ctx)
	if err != nil {
		return "", err
	}
	defer func() {
		if unlockErr := lock.Unlock(); unlockErr != nil {
			ref = ""
			err = ErrUnavailable
		}
	}()
	entries, err := v.entries()
	if err != nil {
		return "", err
	}
	if len(entries) >= 1000 {
		return "", ErrQuota
	}
	var total, connectionBytes int64
	for _, entry := range entries {
		if !mediaRefPattern.MatchString(entry.Name()) {
			continue
		}
		info, statErr := v.files.Lstat(entry.Name())
		if statErr != nil || !info.Mode().IsRegular() {
			return "", ErrDenied
		}
		total += info.Size()
		if strings.HasPrefix(entry.Name(), mediaScopePrefix(scope)) {
			connectionBytes += info.Size()
		}
	}
	ref = mediaScopePrefix(scope) + newID()
	if !mediaRefPattern.MatchString(ref) {
		return "", ErrUnavailable
	}
	blob := MediaBlob{Ref: ref, Scope: scope, Filename: filename, MIME: mime, Data: data, ExpiresAt: v.now().UTC().Add(time.Hour), Authority: authority}
	encoded, err := json.Marshal(blob)
	if err != nil {
		return "", ErrUnavailable
	}
	sealed, err := v.crypto.Encrypt(string(encoded))
	if err != nil || sealed == "" {
		return "", ErrUnavailable
	}
	if total+int64(len(sealed)) > 100<<20 || connectionBytes+int64(len(sealed)) > 50<<20 {
		return "", ErrQuota
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return "", contextErr
	}
	file, err := v.files.OpenFile(ref, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", ErrUnavailable
	}
	_, writeErr := file.WriteString(sealed)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil || ctx.Err() != nil {
		if removeErr := v.files.Remove(ref); removeErr != nil && !os.IsNotExist(removeErr) {
			return "", ErrUnavailable
		}
		return "", ErrUnavailable
	}
	return ref, nil
}

func (v *MediaVault) readBlob(ctx context.Context, ref string) (MediaBlob, error) {
	if err := ctx.Err(); err != nil {
		return MediaBlob{}, err
	}
	if !mediaRefPattern.MatchString(ref) {
		return MediaBlob{}, ErrDenied
	}
	before, err := v.files.Lstat(ref)
	if os.IsNotExist(err) {
		return MediaBlob{}, os.ErrNotExist
	}
	if err != nil || !before.Mode().IsRegular() {
		return MediaBlob{}, ErrDenied
	}
	file, err := v.files.Open(ref)
	if err != nil {
		return MediaBlob{}, ErrUnavailable
	}
	info, statErr := file.Stat()
	if statErr != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) || info.Size() > 4*MaxMediaBytes || info.Mode().Perm()&0o077 != 0 {
		if closeErr := file.Close(); closeErr != nil {
			return MediaBlob{}, ErrUnavailable
		}
		return MediaBlob{}, ErrDenied
	}
	sealed, readErr := io.ReadAll(io.LimitReader(file, 4*MaxMediaBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(sealed) > 4*MaxMediaBytes {
		return MediaBlob{}, ErrUnavailable
	}
	plain, err := v.crypto.Decrypt(string(sealed))
	if err != nil {
		return MediaBlob{}, ErrDenied
	}
	var blob MediaBlob
	if json.Unmarshal([]byte(plain), &blob) != nil || blob.Ref != ref || blob.Scope.validate() != nil || len(blob.Data) == 0 || len(blob.Data) > MaxMediaBytes {
		return MediaBlob{}, ErrDenied
	}
	if err := ctx.Err(); err != nil {
		return MediaBlob{}, err
	}
	return blob, nil
}

// Read authenticates the exact scope and enforces the artifact's own TTL.
func (v *MediaVault) Read(ctx context.Context, scope Scope, ref string) (MediaBlob, error) {
	if scope.validate() != nil {
		return MediaBlob{}, ErrDenied
	}
	blob, err := v.readBlob(ctx, ref)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return MediaBlob{}, ErrDenied
		}
		return MediaBlob{}, err
	}
	if blob.Scope != scope || !blob.ExpiresAt.After(v.now()) {
		return MediaBlob{}, ErrDenied
	}
	return blob, nil
}

// Delete erases one scope-authenticated upload even after TTL/epoch expiry.
func (v *MediaVault) Delete(ctx context.Context, scope Scope, ref string) error {
	if scope.validate() != nil {
		return ErrDenied
	}
	blob, err := v.readBlob(ctx, ref)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if blob.Scope != scope {
		return ErrDenied
	}
	if removeErr := v.files.Remove(ref); removeErr != nil && !os.IsNotExist(removeErr) {
		return ErrUnavailable
	}
	return nil
}

// PurgeExpired removes bounded expired artifact batches without following links.
func (v *MediaVault) PurgeExpired(ctx context.Context) (err error) {
	lock, err := v.quotaLock(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := lock.Unlock(); releaseErr != nil {
			err = ErrUnavailable
		}
	}()
	entries, err := v.entries()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		if !mediaRefPattern.MatchString(entry.Name()) {
			continue
		}
		info, statErr := v.files.Lstat(entry.Name())
		if statErr != nil {
			return ErrUnavailable
		}
		if !info.Mode().IsRegular() {
			return ErrDenied
		}
		if info.ModTime().Before(v.now().Add(-time.Hour)) {
			if removeErr := v.files.Remove(entry.Name()); removeErr != nil && !os.IsNotExist(removeErr) {
				return ErrUnavailable
			}
		}
	}
	return nil
}

// PurgeScope authenticates connection/owner/peer before erasing private uploads.
func (v *MediaVault) PurgeScope(ctx context.Context, scope Scope) (err error) {
	if scope.validate() != nil {
		return ErrDenied
	}
	lock, err := v.quotaLock(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := lock.Unlock(); releaseErr != nil {
			err = ErrUnavailable
		}
	}()
	entries, err := v.entries()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		if !mediaRefPattern.MatchString(entry.Name()) || !strings.HasPrefix(entry.Name(), mediaScopePrefix(scope)) {
			continue
		}
		blob, readErr := v.readBlob(ctx, entry.Name())
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		if readErr != nil {
			return readErr
		}
		if blob.Scope.Key == scope.Key && blob.Scope.OwnerID == scope.OwnerID && (scope.PeerID == 0 || blob.Scope.PeerID == scope.PeerID) {
			if removeErr := v.files.Remove(entry.Name()); removeErr != nil && !os.IsNotExist(removeErr) {
				return ErrUnavailable
			}
		}
	}
	return nil
}

// ReadMedia returns only an authorized current-scope encrypted-vault attachment.
func (s *Service) ReadMedia(ctx context.Context, scope Scope, ref string) (MediaBlob, error) {
	st, err := s.read(ctx, scope.Key)
	if err != nil {
		return MediaBlob{}, err
	}
	if authorityErr := s.available(st, scope); authorityErr != nil {
		return MediaBlob{}, authorityErr
	}
	if s.options.Media == nil {
		return MediaBlob{}, ErrUnsupported
	}
	if !st.Connection.Policy.Enabled || !st.Connection.Policy.Consent {
		return MediaBlob{}, ErrDenied
	}
	peer := st.Peers[scope.PeerID]
	if scope.PeerID > 0 && (peer == nil || !peer.Policy.Enabled || !peer.Policy.Consent) {
		return MediaBlob{}, ErrDenied
	}
	blob, err := s.options.Media.Read(ctx, scope, ref)
	if err != nil {
		return MediaBlob{}, err
	}
	if blob.Authority != MediaAuthorityFor(st.Connection, peer) {
		return MediaBlob{}, ErrStale
	}
	return blob, nil
}
func (s *Service) validateMediaSources(ctx context.Context, scope Scope, method Method, params Params) error {
	type mediaReference struct{ ref, field string }
	var refs []mediaReference
	for _, key := range []string{"photo", "document", "voice", "audio", "video"} {
		if raw := params[key]; raw != nil {
			var ref string
			if json.Unmarshal(raw, &ref) == nil && strings.HasPrefix(ref, "bmedia_") {
				refs = append(refs, mediaReference{ref: ref, field: key})
			}
		}
	}
	if method == SetProfilePhoto || method == PostStory || method == EditStory {
		key := "content"
		if method == SetProfilePhoto {
			key = "photo"
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(params[key], &object) != nil {
			return ErrDenied
		}
		found := false
		for _, field := range []string{"photo", "animation", "video"} {
			if raw := object[field]; raw != nil {
				var ref string
				if json.Unmarshal(raw, &ref) != nil || !mediaRefPattern.MatchString(ref) {
					return ErrDenied
				}
				refs = append(refs, mediaReference{ref: ref, field: field})
				found = true
			}
		}
		if !found {
			return ErrDenied
		}
	}
	for _, reference := range refs {
		if s.options.Media == nil {
			return ErrUnsupported
		}
		blob, err := s.ReadMedia(ctx, scope, reference.ref)
		if err != nil {
			return err
		}
		valid := true
		switch reference.field {
		case "photo":
			valid = blob.MIME == "image/jpeg" || blob.MIME == "image/png"
		case "animation", "video":
			valid = blob.MIME == "video/mp4"
		case "voice":
			valid = blob.MIME == "audio/ogg"
		case "audio":
			valid = blob.MIME == "audio/ogg" || blob.MIME == "audio/mpeg"
		}
		if !valid {
			return ErrDenied
		}
	}
	return nil
}

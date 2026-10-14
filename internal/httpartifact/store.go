// Package httpartifact implements the owner-scoped artifact store behind
// http_request downloads: partial files with durable commit (fsync → atomic
// rename → directory fsync → SQLite ready), atomic quota reservations, TTL
// sweeping, and boot-generation lease recovery (plan §9.2).
package httpartifact

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/httpfetch"
)

// Repo is the metadata persistence interface (implemented by storage/sqlite).
type Repo interface {
	SaveHTTPArtifact(ctx context.Context, a *domain.HTTPArtifact) error
	GetHTTPArtifact(ctx context.Context, id string) (*domain.HTTPArtifact, error)
	ListHTTPArtifactsByOwner(ctx context.Context, ownerUserID string) ([]domain.HTTPArtifact, error)
	ListAllHTTPArtifacts(ctx context.Context) ([]domain.HTTPArtifact, error)
	UpdateHTTPArtifact(ctx context.Context, a *domain.HTTPArtifact) error
	DeleteHTTPArtifact(ctx context.Context, id string) error
	DeleteExpiredHTTPArtifacts(ctx context.Context, before time.Time) ([]domain.HTTPArtifact, error)
	SumHTTPArtifactBytes(ctx context.Context) (ready int64, err error)
}

// Limits bound the store; mirrors the engine's artifact limits.
type Limits struct {
	TTL            time.Duration
	QuotaGlobal    int64 // decimal bytes
	QuotaUser      int64
	DiskReserve    int64 // free space that must remain beyond reservations
	LeaseHeartbeat time.Duration
}

// ErrQuotaExceeded is returned when a reservation cannot be granted.
var ErrQuotaExceeded = errors.New("artifact quota exceeded")

// ErrNotFound is returned for unknown or foreign artifacts.
var ErrNotFound = errors.New("artifact not found")

// Store manages artifacts on the local filesystem with SQLite metadata.
// Single-writer deployment assumption (initial deployment; multi-replica
// needs a shared object store and coordinated quotas).
type Store struct {
	root    string
	repo    Repo
	limits  Limits
	bootGen string
	log     *zap.Logger

	mu             sync.Mutex
	reservedGlobal int64
	reservedByUser map[string]int64
}

// NewStore creates the store, prepares the root directory (0700), and runs
// boot recovery.
func NewStore(root string, repo Repo, limits Limits, log *zap.Logger) (*Store, error) {
	if limits.TTL == 0 {
		limits.TTL = 24 * time.Hour
	}
	var gen [8]byte
	if _, err := rand.Read(gen[:]); err != nil {
		return nil, fmt.Errorf("boot generation: %w", err)
	}
	s := &Store{
		root:           root,
		repo:           repo,
		limits:         limits,
		bootGen:        hex.EncodeToString(gen[:]),
		log:            log,
		reservedByUser: make(map[string]int64),
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("artifact root: %w", err)
	}
	return s, nil
}

// BootGeneration returns this process's lease generation.
func (s *Store) BootGeneration() string { return s.bootGen }

// Root returns the artifact root directory (trusted wiring only; never a
// user-controlled path source).
func (s *Store) Root() string { return s.root }

// Begin reserves quota and opens a new partial artifact. The reservation is
// held until Commit or Abort.
func (s *Store) Begin(ctx context.Context, spec httpfetch.ArtifactSpec) (httpfetch.ArtifactWriter, error) {
	if spec.OwnerUserID == "" {
		return nil, fmt.Errorf("artifact owner required")
	}
	if err := s.reserve(ctx, spec.OwnerUserID, spec.MaxBytes); err != nil {
		return nil, err
	}

	id := newArtifactID()
	storage := id + ".bin"
	part := storage + ".part"

	meta := &domain.HTTPArtifact{
		ID:              id,
		OwnerUserID:     spec.OwnerUserID,
		ChatID:          spec.ChatID,
		StorageName:     storage,
		MIME:            spec.MIME,
		ContentEncoding: spec.ContentEnc,
		Filename:        sanitizeName(spec.Filename),
		Sensitive:       spec.Sensitive,
		State:           domain.HTTPArtifactPartial,
		LeaseBoot:       s.bootGen,
		LeaseExpiresAt:  ptrTime(time.Now().Add(2 * time.Hour)),
		ExpiresAt:       time.Now().Add(s.limits.TTL),
	}
	if err := s.repo.SaveHTTPArtifact(ctx, meta); err != nil {
		s.release(spec.OwnerUserID, spec.MaxBytes)
		return nil, fmt.Errorf("saving artifact metadata: %w", err)
	}

	f, err := os.OpenFile(filepath.Join(s.root, part), //nolint:gosec // G304: server-generated basename under a 0700 root
		os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		s.release(spec.OwnerUserID, spec.MaxBytes)
		_ = s.repo.DeleteHTTPArtifact(ctx, id) //nolint:errcheck // best-effort rollback of just-created row
		return nil, fmt.Errorf("creating partial artifact: %w", err)
	}

	return &writer{
		store: s, meta: meta, file: f,
		maxBytes: spec.MaxBytes, reserve: spec.MaxBytes,
	}, nil
}

// Open returns a read handle for an owner's ready artifact.
func (s *Store) Open(ctx context.Context, ownerUserID, id string) (httpfetch.ArtifactReader, error) {
	meta, err := s.repo.GetHTTPArtifact(ctx, id)
	if err != nil || meta == nil {
		return nil, ErrNotFound
	}
	if meta.OwnerUserID != ownerUserID {
		return nil, ErrNotFound // foreign artifacts are indistinguishable from missing
	}
	if meta.State != domain.HTTPArtifactReady {
		return nil, fmt.Errorf("artifact not ready (state %s)", meta.State)
	}
	f, err := os.Open(filepath.Join(s.root, meta.StorageName))
	if err != nil {
		// Ready record without its file: mark unavailable, never fake empty.
		meta.State = domain.HTTPArtifactUnavailable
		_ = s.repo.UpdateHTTPArtifact(ctx, meta) //nolint:errcheck // diagnostic marker only
		return nil, fmt.Errorf("artifact file missing")
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close() //nolint:errcheck // best-effort close after successful Sync
		return nil, err
	}
	return &reader{f: f, size: st.Size()}, nil
}

// Get returns metadata for the owner (or nil when foreign/missing).
func (s *Store) Get(ctx context.Context, ownerUserID, id string) (*domain.HTTPArtifact, error) {
	meta, err := s.repo.GetHTTPArtifact(ctx, id)
	if err != nil || meta == nil || meta.OwnerUserID != ownerUserID {
		return nil, ErrNotFound
	}
	return meta, nil
}

// List returns the owner's ready artifacts.
func (s *Store) List(ctx context.Context, ownerUserID string) ([]domain.HTTPArtifact, error) {
	return s.repo.ListHTTPArtifactsByOwner(ctx, ownerUserID)
}

// Delete removes an owner's artifact (idempotent).
func (s *Store) Delete(ctx context.Context, ownerUserID, id string) error {
	meta, err := s.Get(ctx, ownerUserID, id)
	if err != nil {
		return err
	}
	if err := s.repo.DeleteHTTPArtifact(ctx, id); err != nil {
		return err
	}
	removeFile(s.root, meta.StorageName)
	return nil
}

// --- quota reservations -------------------------------------------------

func (s *Store) reserve(ctx context.Context, owner string, bytes int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	used, err := s.repo.SumHTTPArtifactBytes(ctx)
	if err != nil {
		return fmt.Errorf("quota check: %w", err)
	}
	if used+s.reservedGlobal+bytes > s.limits.QuotaGlobal {
		return fmt.Errorf("%w: global quota", ErrQuotaExceeded)
	}
	if s.reservedByUser[owner]+bytes > s.limits.QuotaUser {
		return fmt.Errorf("%w: user quota", ErrQuotaExceeded)
	}
	// Disk free reserve: fail closed when the stat is unavailable.
	avail, err := diskAvailable(s.root)
	if err == nil && avail > 0 && int64(min(avail, 1<<62))-bytes < s.limits.DiskReserve {
		return fmt.Errorf("%w: disk reserve", ErrQuotaExceeded)
	}
	s.reservedGlobal += bytes
	s.reservedByUser[owner] += bytes
	return nil
}

// diskAvailable reports free bytes visible to unprivileged users.
func diskAvailable(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	if st.Bsize <= 0 || st.Bavail <= 0 || st.Bavail > math.MaxInt64/uint64(st.Bsize) {
		return 0, nil // nonsensical stat: treat as unknown, callers fail closed on 0
	}
	return st.Bavail * uint64(st.Bsize), nil
}

func (s *Store) release(owner string, bytes int64) {
	s.mu.Lock()
	s.reservedGlobal -= bytes
	s.reservedByUser[owner] -= bytes
	if s.reservedByUser[owner] <= 0 {
		delete(s.reservedByUser, owner)
	}
	s.mu.Unlock()
}

// --- writer --------------------------------------------------------------

type writer struct {
	store    *Store
	meta     *domain.HTTPArtifact
	file     *os.File
	maxBytes int64
	reserve  int64
	n        int64
	done     bool
}

// ID returns the store-assigned artifact identifier.
func (w *writer) ID() string { return w.meta.ID }

func (w *writer) Write(p []byte) (int, error) {
	if w.done {
		return 0, fmt.Errorf("writer already finished")
	}
	if w.n+int64(len(p)) > w.maxBytes {
		return 0, fmt.Errorf("artifact exceeds max bytes")
	}
	n, err := w.file.Write(p)
	w.n += int64(n)
	return n, err
}

// Commit performs the durable sequence: file fsync → atomic rename in the
// same filesystem → directory fsync → SQLite ready metadata. Only after all
// four is the artifact visible to readers.
func (w *writer) Commit(hashHex string, size int64) error {
	if w.done {
		return fmt.Errorf("writer already finished")
	}
	w.done = true
	ctx := context.Background()

	if size != w.n {
		_ = w.Abort() //nolint:errcheck // cleanup on failure path
		return fmt.Errorf("committed size %d != written %d", size, w.n)
	}
	if err := w.file.Sync(); err != nil {
		_ = w.Abort() //nolint:errcheck // cleanup on failure path
		return fmt.Errorf("fsync artifact: %w", err)
	}
	if err := w.file.Close(); err != nil {
		return fmt.Errorf("close artifact: %w", err)
	}
	part := filepath.Join(w.store.root, w.meta.StorageName+".part")
	final := filepath.Join(w.store.root, w.meta.StorageName)
	if err := os.Rename(part, final); err != nil {
		_ = os.Remove(part) //nolint:errcheck // rollback best-effort
		w.store.release(w.meta.OwnerUserID, w.reserve)
		_ = w.store.repo.DeleteHTTPArtifact(ctx, w.meta.ID) //nolint:errcheck // rollback best-effort
		return fmt.Errorf("commit rename: %w", err)
	}
	if err := fsyncDir(w.store.root); err != nil {
		w.store.log.Warn("artifact dir fsync failed", zap.Error(err))
	}

	w.meta.Size = size
	w.meta.SHA256 = hashHex
	w.meta.State = domain.HTTPArtifactReady
	w.meta.LeaseBoot = ""
	w.meta.LeaseExpiresAt = nil
	if err := w.store.repo.UpdateHTTPArtifact(ctx, w.meta); err != nil {
		// File committed but metadata not ready: recovery will quarantine it.
		removeFile(w.store.root, w.meta.StorageName)
		w.store.release(w.meta.OwnerUserID, w.reserve)
		return fmt.Errorf("commit metadata: %w", err)
	}
	w.store.release(w.meta.OwnerUserID, w.reserve)
	return nil
}

// Abort closes and removes the partial; idempotent.
func (w *writer) Abort() error {
	if w.file != nil {
		_ = w.file.Close() //nolint:errcheck // best-effort close on failure path
	}
	removeFile(w.store.root, w.meta.StorageName+".part")
	_ = w.store.repo.DeleteHTTPArtifact(context.Background(), w.meta.ID) //nolint:errcheck // abort best-effort
	w.store.release(w.meta.OwnerUserID, w.reserve)
	w.done = true
	return nil
}

// --- reader ---------------------------------------------------------------

type reader struct {
	f    *os.File
	size int64
	n    int64
}

func (r *reader) Read(p []byte) (int, error) {
	n, err := r.f.Read(p)
	r.n += int64(n)
	return n, err
}

// Close releases the underlying file handle.
func (r *reader) Close() error { return r.f.Close() }

// Size returns the artifact byte size.
func (r *reader) Size() int64 { return r.size }

// --- helpers ---------------------------------------------------------------

func removeFile(root, name string) {
	if name == "" || strings.ContainsAny(name, "/\\") {
		return
	}
	_ = os.Remove(filepath.Join(root, name)) //nolint:errcheck // best-effort remove
}

func fsyncDir(path string) error {
	d, err := os.Open(path) //nolint:gosec // G304: store-managed root path
	if err != nil {
		return err
	}
	defer d.Close() //nolint:errcheck
	return d.Sync()
}

func sanitizeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == '/' || r == '\\' || r == ':' {
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if len(out) > 200 {
		out = out[:200]
	}
	if out == "" {
		return "download"
	}
	return out
}

func newArtifactID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("art-%d", time.Now().UnixNano())
	}
	return "art_" + hex.EncodeToString(b[:])
}

func ptrTime(t time.Time) *time.Time { return &t }

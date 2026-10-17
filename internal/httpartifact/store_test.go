package httpartifact

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/httpfetch"
	"go.uber.org/zap"
)

type memRepo struct {
	mu    sync.Mutex
	rows  map[string]*domain.HTTPArtifact
	order []string
}

func newMemRepo() *memRepo { return &memRepo{rows: make(map[string]*domain.HTTPArtifact)} }

func (m *memRepo) SaveHTTPArtifact(_ context.Context, a *domain.HTTPArtifact) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *a
	m.rows[a.ID] = &cp
	m.order = append(m.order, a.ID)
	return nil
}

func (m *memRepo) GetHTTPArtifact(_ context.Context, id string) (*domain.HTTPArtifact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a, ok := m.rows[id]; ok {
		cp := *a
		return &cp, nil
	}
	return nil, os.ErrNotExist
}

func (m *memRepo) ListHTTPArtifactsByOwner(_ context.Context, owner string) ([]domain.HTTPArtifact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.HTTPArtifact, 0, len(m.order))
	for _, id := range m.order {
		if a := m.rows[id]; a.OwnerUserID == owner {
			out = append(out, *a)
		}
	}
	return out, nil
}

func (m *memRepo) ListAllHTTPArtifacts(_ context.Context) ([]domain.HTTPArtifact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.HTTPArtifact, 0, len(m.order))
	for _, id := range m.order {
		out = append(out, *m.rows[id])
	}
	return out, nil
}

func (m *memRepo) UpdateHTTPArtifact(_ context.Context, a *domain.HTTPArtifact) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.rows[a.ID]; !ok {
		return os.ErrNotExist
	}
	cp := *a
	m.rows[a.ID] = &cp
	return nil
}

func (m *memRepo) DeleteHTTPArtifact(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, id)
	return nil
}

func (m *memRepo) DeleteExpiredHTTPArtifacts(_ context.Context, before time.Time) ([]domain.HTTPArtifact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.HTTPArtifact
	for _, id := range m.order {
		if a := m.rows[id]; a.ExpiresAt.Before(before) {
			out = append(out, *a)
			delete(m.rows, id)
		}
	}
	return out, nil
}

func (m *memRepo) SumHTTPArtifactBytes(_ context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var sum int64
	for _, a := range m.rows {
		if a.State == domain.HTTPArtifactPartial || a.State == domain.HTTPArtifactReady {
			sum += a.Size
		}
	}
	return sum, nil
}

func (m *memRepo) SumHTTPArtifactBytesByOwner(_ context.Context, owner string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var sum int64
	for _, a := range m.rows {
		if a.OwnerUserID == owner && (a.State == domain.HTTPArtifactPartial || a.State == domain.HTTPArtifactReady) {
			sum += a.Size
		}
	}
	return sum, nil
}

func TestOwnerQuotaIncludesCommittedFilesAcrossRestart(t *testing.T) {
	s := testStore(t)
	data := bytes.Repeat([]byte("x"), 500)
	w, err := s.Begin(context.Background(), spec("u1", int64(len(data))))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write(data); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if err = w.Commit(hex.EncodeToString(sum[:]), int64(len(data))); err != nil {
		t.Fatal(err)
	}
	for _, restart := range []bool{false, true} {
		if restart {
			s, err = NewStore(s.Root(), s.repo, s.limits, zap.NewNop())
			if err != nil {
				t.Fatal(err)
			}
		}
		if _, err = s.Begin(context.Background(), spec("u1", 101)); !errors.Is(err, ErrQuotaExceeded) {
			t.Fatalf("committed quota restart=%v: %v", restart, err)
		}
	}
	other, err := s.Begin(context.Background(), spec("u2", 200))
	if err != nil {
		t.Fatal("another owner's quota", err)
	}
	if err = other.Abort(); err != nil {
		t.Fatal(err)
	}
	pending, err := s.Begin(context.Background(), spec("u1", 100))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Begin(context.Background(), spec("u1", 1)); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatal("committed plus in-flight quota", err)
	}
	if err = pending.Abort(); err != nil {
		t.Fatal(err)
	}
}

func testStore(t *testing.T) *Store {
	t.Helper()
	repo := newMemRepo()
	s, err := NewStore(t.TempDir(), repo, Limits{
		TTL: 24 * time.Hour, QuotaGlobal: 1000, QuotaUser: 600, DiskReserve: 0,
	}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func spec(owner string, limit int64) httpfetch.ArtifactSpec {
	return httpfetch.ArtifactSpec{Filename: "a/../b.bin", OwnerUserID: owner, MaxBytes: limit, MIME: "application/octet-stream"}
}

func TestStoreCommitProducesReadyFileWithDurableName(t *testing.T) {
	s := testStore(t)
	w, err := s.Begin(context.Background(), spec("u1", 100))
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("hello artifact")
	if _, werr := w.Write(data); werr != nil {
		t.Fatal(werr)
	}
	sum := sha256.Sum256(data)
	if cerr := w.Commit(hex.EncodeToString(sum[:]), int64(len(data))); cerr != nil {
		t.Fatal(cerr)
	}

	// Owner reads byte-identical content.
	r, err := s.Open(context.Background(), "u1", w.(*writer).ID())
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, r.Size())
	if _, err := readFull(r, buf); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	if !bytes.Equal(buf, data) {
		t.Fatalf("content mismatch: %q", buf)
	}

	// Foreign owner sees nothing.
	if _, err := s.Open(context.Background(), "u2", w.(*writer).ID()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign open: %v", err)
	}

	// No .part leftovers; filename sanitized.
	entries, _ := os.ReadDir(s.Root())
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".part" {
			t.Fatalf("partial left after commit: %s", e.Name())
		}
	}
	meta, _ := s.Get(context.Background(), "u1", w.(*writer).ID())
	if meta.Filename != "a..b.bin" {
		t.Fatalf("filename not sanitized: %q", meta.Filename)
	}
	if meta.State != domain.HTTPArtifactReady || meta.Size != int64(len(data)) {
		t.Fatalf("meta = %+v", meta)
	}
}

func TestStoreAbortCleansPartial(t *testing.T) {
	s := testStore(t)
	w, err := s.Begin(context.Background(), spec("u1", 100))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := w.Abort(); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(s.Root())
	if len(entries) != 0 {
		t.Fatalf("abort left files: %v", entries)
	}
	if _, err := s.Get(context.Background(), "u1", w.(*writer).ID()); !errors.Is(err, ErrNotFound) {
		t.Fatal("metadata row survived abort")
	}

	// Quota released: a new full user-quota begin must succeed.
	if _, err := s.Begin(context.Background(), spec("u1", 600)); err != nil {
		t.Fatalf("quota not released: %v", err)
	}
}

func TestStoreQuotaEnforced(t *testing.T) {
	s := testStore(t)
	// User quota 600: a 500 reservation leaves 100.
	w1, err := s.Begin(context.Background(), spec("u1", 500))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Begin(context.Background(), spec("u1", 200)); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("user quota not enforced: %v", err)
	}
	// Another user still fits under the global 1000 minus 500 reserved.
	if _, err := s.Begin(context.Background(), spec("u2", 500)); err != nil {
		t.Fatalf("global quota miscounted: %v", err)
	}
	_ = w1.Abort()
}

func TestSweepRecoversOrphansAndStaleLeases(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// Committed file without metadata (crash between rename and DB commit).
	orphan := filepath.Join(s.Root(), "art_orphan.bin")
	if err := os.WriteFile(orphan, []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Stale .part from a past boot generation (writer process gone).
	stale := &domain.HTTPArtifact{
		ID: "art_stale", OwnerUserID: "u1", StorageName: "art_stale.bin",
		State: domain.HTTPArtifactPartial, LeaseBoot: "oldboot",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := s.repo.SaveHTTPArtifact(ctx, stale); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Root(), "art_stale.bin.part"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Live partial of THIS boot: must survive.
	live, err := s.Begin(ctx, spec("u1", 50))
	if err != nil {
		t.Fatal(err)
	}

	s.Sweep(ctx)

	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("orphan committed file survived sweep")
	}
	if _, err := os.Stat(filepath.Join(s.Root(), "art_stale.bin.part")); !os.IsNotExist(err) {
		t.Fatal("stale-boot partial survived sweep")
	}
	if _, err := os.Stat(filepath.Join(s.Root(), live.(*writer).meta.StorageName+".part")); err != nil {
		t.Fatalf("live partial removed by sweep: %v", err)
	}
}

func readFull(r io.Reader, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

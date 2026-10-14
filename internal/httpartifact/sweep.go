package httpartifact

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/domain"
)

// StartSweeper runs TTL cleanup every interval until ctx is done. The first
// pass runs immediately (startup recovery included).
func (s *Store) StartSweeper(ctx context.Context, interval time.Duration) {
	go func() {
		s.Sweep(ctx)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.Sweep(ctx)
			}
		}
	}()
}

// Sweep applies the deterministic recovery rules (plan §9.2):
//   - expired ready artifacts are deleted (file + row);
//   - .part files without a live lease (same boot generation and unexpired)
//     are removed — past-process leases are never live;
//   - committed files without ready metadata are quarantined by deletion;
//   - ready rows without their file become unavailable, not empty successes;
//   - quota counters are recomputed from the database.
func (s *Store) Sweep(ctx context.Context) {
	now := time.Now()

	// 1. TTL deletions.
	expired, err := s.repo.DeleteExpiredHTTPArtifacts(ctx, now)
	if err != nil {
		s.log.Warn("artifact sweep: ttl query failed", zap.Error(err))
	}
	for i := range expired {
		removeFile(s.root, expired[i].StorageName)
	}

	// 2. Filesystem reconciliation: every file must have a live row.
	known := s.knownStorageNames(ctx)
	entries, err := os.ReadDir(s.root)
	if err != nil {
		s.log.Warn("artifact sweep: read root failed", zap.Error(err))
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(name, ".part") {
			base := strings.TrimSuffix(name, ".part")
			if !s.leaseLive(ctx, base) {
				removeFile(s.root, name)
				_ = s.repo.DeleteHTTPArtifact(ctx, base) //nolint:errcheck // sweep: row is going away regardless
			}
			continue
		}
		// Committed file: must belong to a ready/unavailable row.
		meta, ok := known[name]
		if !ok || (meta.State != domain.HTTPArtifactReady && meta.State != domain.HTTPArtifactUnavailable) {
			// Committed bytes without durable ready metadata: quarantine by
			// deletion (never expose unverifiable content).
			removeFile(s.root, name)
			if ok {
				_ = s.repo.DeleteHTTPArtifact(ctx, meta.ID) //nolint:errcheck // sweep: quarantine
			}
		}
	}

	// 3. Ready rows without files → unavailable.
	for name := range known {
		meta := known[name]
		if meta.State != domain.HTTPArtifactReady {
			continue
		}
		if _, err := os.Stat(filepath.Join(s.root, meta.StorageName)); os.IsNotExist(err) {
			meta.State = domain.HTTPArtifactUnavailable
			if err := s.repo.UpdateHTTPArtifact(ctx, &meta); err != nil {
				s.log.Warn("artifact sweep: mark unavailable failed", zap.String("id", meta.ID), zap.Error(err))
			}
			known[name] = meta
		}
	}

	// 4. Reset in-memory reservations (single-writer: boot-time truth).
	s.mu.Lock()
	s.reservedGlobal = 0
	s.reservedByUser = make(map[string]int64)
	s.mu.Unlock()
}

func (s *Store) knownStorageNames(ctx context.Context) map[string]domain.HTTPArtifact {
	out := make(map[string]domain.HTTPArtifact)
	all, err := s.repo.ListAllHTTPArtifacts(ctx)
	if err != nil {
		s.log.Warn("artifact sweep: list failed", zap.Error(err))
		return out
	}
	for i := range all {
		out[all[i].StorageName] = all[i]
	}
	return out
}

// leaseLive reports whether a partial file (by storage name) still has a
// live writer lease: same boot generation and unexpired. Past-boot leases
// are stale — the writing process is gone.
func (s *Store) leaseLive(ctx context.Context, storageName string) bool {
	all, err := s.repo.ListAllHTTPArtifacts(ctx)
	if err != nil {
		// Fail safe: without metadata we cannot prove liveness.
		return false
	}
	for i := range all {
		meta := &all[i]
		if meta.StorageName != storageName {
			continue
		}
		if meta.LeaseBoot != s.bootGen {
			return false
		}
		if meta.LeaseExpiresAt != nil && meta.LeaseExpiresAt.Before(time.Now()) {
			return false
		}
		return true
	}
	return false
}

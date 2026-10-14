package httpfetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/skill/interact"
)

// memArtifactStore is a minimal in-memory ArtifactStore for engine tests.
type memArtifactStore struct {
	files      map[string][]byte
	commits    atomic.Int64
	beginFails bool
}

func newMemArtifactStore() *memArtifactStore {
	return &memArtifactStore{files: make(map[string][]byte)}
}

type memWriter struct {
	store *memArtifactStore
	id    string
	buf   []byte
	max   int64
}

func (w *memWriter) ID() string { return w.id }
func (w *memWriter) Write(p []byte) (int, error) {
	if int64(len(w.buf)+len(p)) > w.max {
		return 0, fmt.Errorf("artifact exceeds max bytes")
	}
	w.buf = append(w.buf, p...)
	return len(p), nil
}
func (w *memWriter) Commit(hash string, size int64) error {
	if size != int64(len(w.buf)) {
		return fmt.Errorf("size mismatch")
	}
	w.store.files[w.id] = w.buf
	w.store.commits.Add(1)
	return nil
}
func (w *memWriter) Abort() error { return nil }

func (s *memArtifactStore) Begin(_ context.Context, spec ArtifactSpec) (ArtifactWriter, error) {
	if s.beginFails {
		return nil, fmt.Errorf("store full")
	}
	w := &memWriter{store: s, id: "art_" + fmt.Sprint(len(s.files)+1), max: spec.MaxBytes}
	s.files[w.id] = nil
	return w, nil
}
func (s *memArtifactStore) Open(_ context.Context, owner, id string) (ArtifactReader, error) {
	data, ok := s.files[id]
	if !ok || owner == "" {
		return nil, fmt.Errorf("not found")
	}
	return &memReader{data: data}, nil
}
func (s *memArtifactStore) Delete(_ context.Context, owner, id string) error {
	delete(s.files, id)
	return nil
}

type memReader struct {
	data []byte
	pos  int
}

func (r *memReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, fmt.Errorf("EOF")
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}
func (r *memReader) Close() error { return nil }
func (r *memReader) Size() int64  { return int64(len(r.data)) }

// downloadLimits keeps every threshold tiny so consent boundaries are
// testable without large fixtures.
func downloadLimits() Limits {
	l := DefaultLimits()
	l.ConsentThresholdBytes = 100
	l.MaxDownloadBytes = 1000
	l.InlineMaxBytes = 2 << 20
	l.WallTimeout = 10 * time.Second
	l.RequestTimeout = 5 * time.Second
	return l
}

func newDownloadEngine(t *testing.T, proxyURL string, confirmer Confirmer, store ArtifactStore) *Engine {
	t.Helper()
	e, perr := NewEngine(EngineConfig{
		ProxyURL:  proxyURL,
		Limits:    downloadLimits(),
		Policy:    Policy{Enabled: true},
		Resolver:  fakeResolver{},
		Confirmer: confirmer,
		Artifacts: store,
	})
	if perr != nil {
		t.Fatal(perr)
	}
	return e
}

const downloadPlanFmt = `{
	"version": 1,
	"request": {
		"id": "dl", "method": "GET", "url": "http://api.example.test/files/export",
		"response": {"mode": "download", "filename": "export.bin"}
	}
}`

func TestDownloadKnownLargeRequiresConsentBeforeRead(t *testing.T) {
	body := strings.Repeat("x", 500) // > threshold(100), < hard cap(1000)
	var served atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served.Store(true)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	// Declined: no artifact, no ready commit.
	store := newMemArtifactStore()
	e := newDownloadEngine(t, srv.URL, decliningConfirmer{}, store)
	res := runPlan(t, e, downloadPlanFmt)
	if res.Outcome != OutcomeDeclined {
		t.Fatalf("outcome = %s, want declined", res.Outcome)
	}
	if store.commits.Load() != 0 {
		t.Fatal("declined download committed an artifact")
	}

	// Approved: full body stored with correct hash.
	conf := &approvingConfirmer{}
	store2 := newMemArtifactStore()
	e2 := newDownloadEngine(t, srv.URL, conf, store2)
	res2 := runPlan(t, e2, downloadPlanFmt)
	if res2.Outcome != OutcomeCompleted {
		t.Fatalf("outcome = %s (%+v)", res2.Outcome, res2.Error)
	}
	st := res2.Steps[0]
	if st.Artifact == nil || st.Artifact.Bytes != 500 {
		t.Fatalf("artifact = %+v", st.Artifact)
	}
	sum := sha256.Sum256([]byte(body))
	if st.Artifact.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("hash mismatch: %s", st.Artifact.SHA256)
	}
	if len(conf.asked) != 1 || conf.asked[0] != interact.ConfirmLargeDownload {
		t.Fatalf("consent reasons = %v", conf.asked)
	}
	if !served.Load() {
		t.Fatal("origin never served the request")
	}
}

func TestDownloadUnknownSizePausesAtThreshold(t *testing.T) {
	// No Content-Length (chunked): the engine must stop reading at exactly
	// the threshold and ask before continuing on the SAME stream.
	body := strings.Repeat("y", 350)
	var readPast atomic.Int64 // bytes consumed by handler writes
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		// Chunked: write in pieces without flushing length.
		for i := 0; i < len(body); i += 50 {
			chunk := body[i:min(i+50, len(body))]
			_, _ = w.Write([]byte(chunk))
			readPast.Add(int64(len(chunk)))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	defer srv.Close()

	conf := &approvingConfirmer{}
	store := newMemArtifactStore()
	e := newDownloadEngine(t, srv.URL, conf, store)
	res := runPlan(t, e, downloadPlanFmt)
	if res.Outcome != OutcomeCompleted {
		t.Fatalf("outcome = %s (%+v)", res.Outcome, res.Error)
	}
	if store.commits.Load() != 1 {
		t.Fatal("expected exactly one committed artifact")
	}
	if len(conf.asked) != 1 {
		t.Fatalf("expected one threshold consent, got %v", conf.asked)
	}
	st := res.Steps[0]
	if st.Artifact == nil || st.Artifact.Bytes != 350 {
		t.Fatalf("artifact = %+v", st.Artifact)
	}
}

func TestDownloadOverHardCapRefusedWithoutAsking(t *testing.T) {
	body := strings.Repeat("z", 2000) // > hard cap 1000, > threshold 100
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	conf := &approvingConfirmer{}
	store := newMemArtifactStore()
	e := newDownloadEngine(t, srv.URL, conf, store)
	res := runPlan(t, e, downloadPlanFmt)
	if res.Outcome != OutcomeQuotaExceeded {
		t.Fatalf("outcome = %s, want quota_exceeded", res.Outcome)
	}
	if len(conf.asked) != 0 {
		t.Fatal("over-cap download must be refused without a consent question")
	}
	if store.commits.Load() != 0 {
		t.Fatal("over-cap download committed")
	}
}

func TestDownloadUnknownSizeOverGrantedLimitStops(t *testing.T) {
	// Stream larger than the hard cap with unknown length: after consent the
	// transfer stops at the cap with a structured quota error.
	body := strings.Repeat("w", 2000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		for i := 0; i < len(body); i += 100 {
			_, _ = w.Write([]byte(body[i : i+100]))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	defer srv.Close()

	conf := &approvingConfirmer{}
	store := newMemArtifactStore()
	e := newDownloadEngine(t, srv.URL, conf, store)
	res := runPlan(t, e, downloadPlanFmt)
	if res.Outcome != OutcomeQuotaExceeded {
		t.Fatalf("outcome = %s, want quota_exceeded at hard cap", res.Outcome)
	}
	if store.commits.Load() != 0 {
		t.Fatal("partial artifact was committed as successful")
	}
}

func TestDownloadWithoutStoreFailsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("data"))
	}))
	defer srv.Close()
	e := newDownloadEngine(t, srv.URL, nil, nil)
	res := runPlan(t, e, downloadPlanFmt)
	if res.Outcome != OutcomePolicyDenied {
		t.Fatalf("outcome = %s, want policy_denied", res.Outcome)
	}
	if res.Error == nil || res.Error.Code != "artifact_delivery_unavailable" {
		t.Fatalf("error = %+v", res.Error)
	}
}

func TestDownloadStoresOpaqueGzipBytes(t *testing.T) {
	// Content-Encoding responses are stored as received (opaque): the stored
	// bytes are the wire bytes, not the decompressed entity.
	encBody := "gzip-bytes-opaque-representation-123"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", fmt.Sprint(len(encBody)))
		_, _ = w.Write([]byte(encBody))
	}))
	defer srv.Close()

	store := newMemArtifactStore()
	e := newDownloadEngine(t, srv.URL, &approvingConfirmer{}, store)
	res := runPlan(t, e, downloadPlanFmt)
	if res.Outcome != OutcomeCompleted {
		t.Fatalf("outcome = %s (%+v)", res.Outcome, res.Error)
	}
	st := res.Steps[0]
	if st.Artifact == nil || st.Artifact.Bytes != int64(len(encBody)) {
		t.Fatalf("artifact = %+v", st.Artifact)
	}
}

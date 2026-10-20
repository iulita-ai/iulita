package remoteaccess

// The worker protocol deliberately has no filesystem, shell, forwarding, or
// vault API. The controller authorizes the operation; the worker additionally
// enforces deployment-owned destination networks before native SSH connects.
import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const workerMaxBody = 16 << 20
const workerMaxResponse = 16 << 20

type workerEnvelope struct {
	ID       string           `json:"id"`
	IssuedAt time.Time        `json:"issued_at"`
	Request  TransportRequest `json:"request"`
}
type workerReply struct {
	Result TransportResult `json:"result"`
	Failed bool            `json:"failed"`
}

// RPCTransport never retries a request: an HTTP failure after transmission is
// ambiguous even when no SSH acknowledgement made it back to the controller.
type RPCTransport struct {
	endpoint string
	key      []byte
	client   *http.Client
}

// NewRPCTransport creates a no-retry client over TLS or a private Unix socket.
func NewRPCTransport(endpoint string, key []byte) (*RPCTransport, error) {
	if len(key) < 32 {
		return nil, errors.New("SSH worker key must have at least 32 bytes")
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("default HTTP transport unavailable")
	}
	t := base.Clone()
	t.Proxy = nil
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, errors.New("invalid SSH worker endpoint")
	}
	switch u.Scheme {
	case "unix":
		if !filepath.IsAbs(u.Path) || u.Host != "" {
			return nil, errors.New("SSH worker unix socket must be absolute")
		}
		socket := u.Path
		t.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}
		endpoint = "http://ssh-worker/execute"
	case "https":
		if u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("invalid SSH worker HTTPS endpoint")
		}
		endpoint = strings.TrimRight(endpoint, "/") + "/execute"
	default:
		return nil, errors.New("SSH worker requires HTTPS or a private unix socket")
	}
	return &RPCTransport{endpoint: endpoint, key: append([]byte(nil), key...), client: &http.Client{Transport: t, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func signature(key, body []byte) string {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// Run sends a single authenticated request without replay on transport failure.
func (t *RPCTransport) Run(ctx context.Context, req TransportRequest) (TransportResult, error) {
	envelope := workerEnvelope{ID: uuid.NewString(), IssuedAt: time.Now().UTC(), Request: req}
	data, err := json.Marshal(envelope)
	if err != nil || len(data) > workerMaxBody {
		return TransportResult{}, errors.New("SSH worker request too large")
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	renewalCtx, stopRenewal := context.WithCancel(ctx)
	defer stopRenewal()
	go t.renewLease(renewalCtx, envelope.ID)
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(data))
	if err != nil {
		return TransportResult{}, err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Iulita-Signature", signature(t.key, data))
	resp, err := t.client.Do(r)
	if err != nil {
		return TransportResult{Dispatched: true, ErrorCode: "worker_outcome_unknown"}, errors.New("SSH worker connection lost; remote outcome unknown")
	}
	defer closeRPCResource(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return TransportResult{Dispatched: true, ErrorCode: "worker_outcome_unknown"}, errors.New("SSH worker rejected request or lost response")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, workerMaxResponse+1))
	if err != nil || len(body) > workerMaxResponse {
		return TransportResult{Dispatched: true, ErrorCode: "worker_outcome_unknown"}, errors.New("SSH worker response unavailable")
	}
	var reply workerReply
	if err = json.Unmarshal(body, &reply); err != nil {
		return TransportResult{Dispatched: true, ErrorCode: "worker_outcome_unknown"}, errors.New("invalid SSH worker response")
	}
	if reply.Failed {
		return reply.Result, errors.New("SSH execution failed")
	}
	return reply.Result, nil
}

// WorkerHandler must be served behind TLS or on a private unix socket. Nonces
// are durably claimed before dispatch, including across worker restarts.
type WorkerHandler struct {
	key       []byte
	prefixes  []netip.Prefix
	stateDir  string
	transport interface {
		Run(context.Context, TransportRequest) (TransportResult, error)
	}
	slots   chan struct{}
	mu      sync.Mutex
	leases  map[string]workerLease
	claimMu sync.Mutex
}

// NewWorkerHandler validates destination networks and durable replay storage.
func NewWorkerHandler(key []byte, cidrs []string, stateDir string) (*WorkerHandler, error) {
	if len(key) < 32 {
		return nil, errors.New("SSH worker key must have at least 32 bytes")
	}
	if !filepath.IsAbs(stateDir) {
		return nil, errors.New("SSH worker state directory must be absolute")
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(stateDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("SSH worker state directory must be private (0o700)")
	}
	h := &WorkerHandler{key: append([]byte(nil), key...), stateDir: stateDir, transport: &NativeTransport{}, slots: make(chan struct{}, 2), leases: map[string]workerLease{}}
	for _, c := range cidrs {
		p, err := netip.ParsePrefix(strings.TrimSpace(c))
		if err != nil {
			return nil, fmt.Errorf("invalid SSH worker network: %w", err)
		}
		h.prefixes = append(h.prefixes, p)
	}
	if len(h.prefixes) == 0 {
		return nil, errors.New("SSH worker allowed networks are required")
	}
	return h, nil
}
func (h *WorkerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/terminal" {
		h.ServePTY(w, r)
		return
	}
	if r.URL.Path == "/renew" {
		h.renew(w, r)
		return
	}
	if r.Method != http.MethodPost || r.URL.Path != "/execute" {
		http.NotFound(w, r)
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		http.Error(w, "worker busy", http.StatusTooManyRequests)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, workerMaxBody+1))
	if err != nil || len(body) > workerMaxBody {
		http.Error(w, "request rejected", http.StatusBadRequest)
		return
	}
	if !validSignature(h.key, body, r.Header.Get("X-Iulita-Signature")) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var envelope workerEnvelope
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if dec.Decode(&envelope) != nil || envelope.ID == "" || len(envelope.ID) > 128 || time.Since(envelope.IssuedAt) > 30*time.Second || time.Until(envelope.IssuedAt) > time.Second {
		http.Error(w, "request expired or malformed", http.StatusBadRequest)
		return
	}

	if len(envelope.Request.Endpoints) < 1 || len(envelope.Request.Endpoints) > 4 {
		http.Error(w, "invalid route", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Minute)
	defer cancel()
	for i := range envelope.Request.Endpoints {
		ep := &envelope.Request.Endpoints[i]
		ips, resolveErr := net.DefaultResolver.LookupNetIP(ctx, "ip", ep.Address)
		if resolveErr != nil || len(ips) == 0 {
			http.Error(w, "destination unavailable", http.StatusForbidden)
			return
		}
		// Validate every answer, then connect to a numeric address. In particular,
		// a downstream bastion must not resolve the name differently a second time.
		for _, ip := range ips {
			allowed := false
			ip = ip.Unmap()
			for _, p := range h.prefixes {
				if p.Contains(ip) {
					allowed = true
					break
				}
			}
			if !allowed {
				http.Error(w, "destination denied", http.StatusForbidden)
				return
			}
		}
		ep.Address = ips[0].Unmap().String()
	}
	if time.Since(envelope.IssuedAt) > workerLeaseTTL || ctx.Err() != nil {
		http.Error(w, "request expired", http.StatusBadRequest)
		return
	}
	if err = h.claim(envelope.ID); err != nil {
		http.Error(w, "request already claimed or unavailable", http.StatusConflict)
		return
	}
	if time.Since(envelope.IssuedAt) > workerLeaseTTL || ctx.Err() != nil {
		http.Error(w, "lease expired before dispatch", http.StatusBadRequest)
		return
	}
	release := h.withLease(ctx, cancel, envelope.ID, envelope.IssuedAt)
	defer release()
	result, runErr := h.transport.Run(ctx, envelope.Request)
	w.Header().Set("Content-Type", "application/json")
	if encodeErr := json.NewEncoder(w).Encode(workerReply{Result: result, Failed: runErr != nil}); encodeErr != nil {
		return
	}
}
func (h *WorkerHandler) claim(id string) error {
	h.claimMu.Lock()
	defer h.claimMu.Unlock()
	entries, err := os.ReadDir(h.stateDir)
	if err != nil {
		return err
	}
	live := 0
	for _, entry := range entries {
		info, statErr := entry.Info()
		if statErr != nil {
			return statErr
		}
		if info.Mode().IsRegular() && time.Since(info.ModTime()) > 24*time.Hour {
			if removeErr := os.Remove(filepath.Join(h.stateDir, entry.Name())); removeErr != nil {
				return removeErr
			}
		} else {
			live++
		}
	}
	if live >= 100000 {
		return errors.New("SSH worker nonce quota reached")
	}
	sum := sha256.Sum256([]byte(id))
	path := filepath.Join(h.stateDir, hex.EncodeToString(sum[:]))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- path is a SHA-256 nonce under the configured private state directory.
	if err != nil {
		return err
	}
	_, err = f.WriteString(time.Now().UTC().Format(time.RFC3339Nano))
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
	dir, err := os.Open(h.stateDir)
	if err != nil {
		return err
	}
	defer closeRPCResource(dir)
	return dir.Sync()
}

// Cleanup failures cannot change a completed remote command's outcome.
func closeRPCResource(c io.Closer) {
	if err := c.Close(); err != nil {
		return
	}
}

func validSignature(key, body []byte, provided string) bool {
	return subtle.ConstantTimeCompare([]byte(signature(key, body)), []byte(provided)) == 1
}

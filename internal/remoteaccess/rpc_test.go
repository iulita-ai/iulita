package remoteaccess

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type rpcFake struct{ calls atomic.Int32 }

func (f *rpcFake) Run(_ context.Context, r TransportRequest) (TransportResult, error) {
	f.calls.Add(1)
	code := 0
	return TransportResult{Dispatched: true, ExitCode: &code, Stdout: []byte(r.Endpoints[0].Address)}, nil
}
func workerFixture(t *testing.T) (*WorkerHandler, []byte, *rpcFake) {
	t.Helper()
	key := bytes.Repeat([]byte{42}, 32)
	dir := filepath.Join(t.TempDir(), "nonces")
	h, err := NewWorkerHandler(key, []string{"127.0.0.0/8"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	f := &rpcFake{}
	h.transport = f
	return h, key, f
}
func signedRequest(t *testing.T, h http.Handler, key []byte, e workerEnvelope) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/execute", bytes.NewReader(data))
	r.Header.Set("X-Iulita-Signature", signature(key, data))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func rpcEnvelope(id, address string) workerEnvelope {
	return workerEnvelope{ID: id, IssuedAt: time.Now(), Request: TransportRequest{Endpoints: []TransportEndpoint{{Address: address, Port: 22}}, Command: "true"}}
}
func TestWorkerAuthorizationAndDurableReplay(t *testing.T) {
	h, key, f := workerFixture(t)
	e := rpcEnvelope("one", "127.0.0.1")
	w := signedRequest(t, h, []byte("wrong"), e)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	w = signedRequest(t, h, key, e)
	if w.Code != 200 || f.calls.Load() != 1 {
		t.Fatalf("%d %d", w.Code, f.calls.Load())
	}
	w = signedRequest(t, h, key, e)
	if w.Code != 409 || f.calls.Load() != 1 {
		t.Fatalf("replay %d %d", w.Code, f.calls.Load())
	}
	restarted, err := NewWorkerHandler(key, []string{"127.0.0.0/8"}, h.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	restarted.transport = f
	w = signedRequest(t, restarted, key, e)
	if w.Code != 409 || f.calls.Load() != 1 {
		t.Fatal("replay after restart")
	}
}
func TestWorkerDeniesNetworkAndExpired(t *testing.T) {
	h, key, f := workerFixture(t)
	e := rpcEnvelope("denied", "192.0.2.1")
	if w := signedRequest(t, h, key, e); w.Code != 403 {
		t.Fatal(w.Code)
	}
	e = rpcEnvelope("old", "127.0.0.1")
	e.IssuedAt = time.Now().Add(-time.Minute)
	if w := signedRequest(t, h, key, e); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if f.calls.Load() != 0 {
		t.Fatal("unexpected dispatch")
	}
}
func TestRPCRequiresSecureTransportAndNoRetry(t *testing.T) {
	key := bytes.Repeat([]byte{42}, 32)
	if _, err := NewRPCTransport("http://127.0.0.1", key); err == nil {
		t.Fatal("plaintext accepted")
	}
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(502) }))
	defer server.Close()
	client, err := NewRPCTransport(server.URL, key)
	if err != nil {
		t.Fatal(err)
	}
	client.client = server.Client()
	result, err := client.Run(context.Background(), TransportRequest{Command: "true"})
	if err == nil || !result.Dispatched || calls.Load() != 1 {
		t.Fatalf("no unknown or retry: %+v %v %d", result, err, calls.Load())
	}
}
func TestRPCIsolatedEndToEnd(t *testing.T) {
	h, key, f := workerFixture(t)
	server := httptest.NewTLSServer(h)
	defer server.Close()
	client, err := NewRPCTransport(server.URL, key)
	if err != nil {
		t.Fatal(err)
	}
	client.client = server.Client()
	out, err := client.Run(context.Background(), rpcEnvelope("unused", "127.0.0.1").Request)
	if err != nil || out.ExitCode == nil || string(out.Stdout) != "127.0.0.1" || f.calls.Load() != 1 {
		t.Fatalf("%+v %v", out, err)
	}
}
func TestWorkerRejectsUnsafeStateDir(t *testing.T) {
	key := bytes.Repeat([]byte{42}, 32)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := NewWorkerHandler(key, []string{"127.0.0.0/8"}, dir); err == nil {
		t.Fatal("public directory accepted")
	}
	if _, err := NewWorkerHandler(key, nil, filepath.Join(dir, "private")); err == nil {
		t.Fatal("unrestricted network accepted")
	}
}
func TestWorkerRejectsBodyFlood(t *testing.T) {
	h, _, _ := workerFixture(t)
	r := httptest.NewRequest("POST", "/execute", strings.NewReader(strings.Repeat("x", workerMaxBody+1)))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
}

type leaseWaitingTransport struct{}

func (leaseWaitingTransport) Run(ctx context.Context, _ TransportRequest) (TransportResult, error) {
	<-ctx.Done()
	return TransportResult{Dispatched: true, ErrorCode: "lease_expired"}, ctx.Err()
}
func TestWorkerLeaseExpiresWithoutController(t *testing.T) {
	h, key, _ := workerFixture(t)
	h.transport = leaseWaitingTransport{}
	e := rpcEnvelope("expired-active-lease", "127.0.0.1")
	e.IssuedAt = time.Now().Add(-workerLeaseTTL + 200*time.Millisecond)
	started := time.Now()
	w := signedRequest(t, h, key, e)
	if time.Since(started) > time.Second || w.Code != 200 {
		t.Fatalf("lease failed to cancel: %d", w.Code)
	}
	var reply workerReply
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if !reply.Failed || !reply.Result.Dispatched {
		t.Fatal("expiry claimed remote success")
	}
}
func TestWorkerRenewalCannotReplayToExtendForever(t *testing.T) {
	h, key, _ := workerFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := h.withLease(ctx, cancel, "active", time.Now().Add(-time.Second))
	defer release()
	renewal := workerRenewal{ID: "active", IssuedAt: time.Now(), Kind: "renew"}
	data, err := json.Marshal(renewal)
	if err != nil {
		t.Fatal(err)
	}
	renew := func() int {
		r := httptest.NewRequest("POST", "/renew", bytes.NewReader(data))
		r.Header.Set("X-Iulita-Signature", signature(key, data))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if renew() != 204 {
		t.Fatal("renewal denied")
	}
	h.mu.Lock()
	first := h.leases["active"].expires
	h.mu.Unlock()
	if renew() != 204 {
		t.Fatal("idempotent renewal failed")
	}
	h.mu.Lock()
	second := h.leases["active"].expires
	h.mu.Unlock()
	if !first.Equal(second) {
		t.Fatal("replay extended lease")
	}
}

func TestServiceThroughTLSWorkerAndNativeMultihop(t *testing.T) {
	svc, scope, request, _ := setupService(t)
	snapshot := svc.Snapshot(scope)
	var identity Identity
	for _, candidate := range snapshot.Identities {
		identity = candidate
		break
	}
	identity.Username = "test"
	var err error
	identity, err = svc.PutIdentity(context.Background(), scope, identity, &Secret{Password: "password"})
	if err != nil {
		t.Fatal(err)
	}
	hops := make([]Hop, 0, 3)
	for range 3 {
		endpoint, _ := testSSHServer(t)
		server, putErr := svc.PutServer(scope, Server{Address: endpoint.Address, Port: endpoint.Port, HostKeys: endpoint.HostKeys, TrustSource: "independent test inventory", Enabled: true})
		if putErr != nil {
			t.Fatal(putErr)
		}
		hops = append(hops, Hop{ServerID: server.ID, IdentityID: identity.ID})
	}
	route, err := svc.PutRoute(scope, Route{ID: request.RouteID, Hops: hops, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.PutGrant(scope, Grant{RouteID: route.ID, CallerID: scope.CallerID, Capability: "exec", Enabled: true, ExpiresAt: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	worker, key, _ := workerFixture(t)
	worker.transport = &NativeTransport{}
	server := httptest.NewTLSServer(worker)
	defer server.Close()
	client, err := NewRPCTransport(server.URL, key)
	if err != nil {
		t.Fatal(err)
	}
	client.client = server.Client()
	svc.transport = client
	result, err := svc.Execute(context.Background(), scope, request, func(context.Context, Approval) (bool, error) { return true, nil })
	if err != nil || result.State != "completed" || result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("end-to-end %+v %v", result, err)
	}
	output, err := svc.Artifact(scope, result.ArtifactID)
	if err != nil || output.Stdout != "ok" {
		t.Fatalf("private output %+v %v", output, err)
	}
}

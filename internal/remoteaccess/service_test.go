package remoteaccess

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/credential"
	"github.com/iulita-ai/iulita/internal/domain"
	"go.uber.org/zap"
	"golang.org/x/crypto/ssh"
)

type privateRepo struct {
	credential.Repository
	mu sync.Mutex
	c  map[string]*domain.Credential
}

func (r *privateRepo) GetCredentialByNameAndOwner(_ context.Context, n, o string) (*domain.Credential, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v := r.c[n]
	if v != nil && v.OwnerID == o {
		cp := *v
		return &cp, nil
	}
	return nil, nil
}
func (r *privateRepo) SaveCredential(_ context.Context, c *domain.Credential) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.c[c.Name] = c
	return nil
}
func (r *privateRepo) UpdateCredential(ctx context.Context, c *domain.Credential) error {
	return r.SaveCredential(ctx, c)
}
func (r *privateRepo) SaveCredentialAudit(context.Context, *domain.CredentialAudit) error { return nil }

type testTransport struct {
	calls int
	run   func(context.Context) (TransportResult, error)
}

func (t *testTransport) Run(ctx context.Context, _ TransportRequest) (TransportResult, error) {
	t.calls++
	if t.run != nil {
		return t.run(ctx)
	}
	code := 0
	return TransportResult{Dispatched: true, ExitCode: &code, Stdout: []byte("SECRET-CANARY")}, nil
}
func setupService(t *testing.T) (*Service, Scope, ExecuteRequest, *testTransport) {
	t.Helper()
	crypto, e := config.NewEncryptor(make([]byte, 32))
	if e != nil {
		t.Fatal(e)
	}
	vault := credential.NewStore(&privateRepo{c: map[string]*domain.Credential{}}, crypto, zap.NewNop())
	tr := &testTransport{}
	s, e := NewService(filepath.Join(t.TempDir(), "catalog.json"), vault, tr)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	sc := Scope{"owner", "ssh_exec"}
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	pk, _ := ssh.NewPublicKey(pub)
	v, e := s.PutServer(sc, Server{Address: "127.0.0.1", Port: 22, HostKeys: []string{string(ssh.MarshalAuthorizedKey(pk))}, TrustSource: "console", Enabled: true})
	if e != nil {
		t.Fatal(e)
	}
	i, e := s.PutIdentity(context.Background(), sc, Identity{Username: "alice", AuthType: "password", Enabled: true}, &Secret{Password: "private-password"}) // gitleaks:allow -- Synthetic fixture credential, never used with real servers.
	if e != nil {
		t.Fatal(e)
	}
	r, e := s.PutRoute(sc, Route{Hops: []Hop{{v.ID, i.ID}}, Enabled: true})
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.PutGrant(sc, Grant{CallerID: sc.CallerID, RouteID: r.ID, Capability: "exec", Enabled: true, ExpiresAt: time.Now().Add(time.Hour)})
	if e != nil {
		t.Fatal(e)
	}
	s.SetEnabled(true)
	return s, sc, ExecuteRequest{RouteID: r.ID, Command: "printf hello"}, tr
}
func TestExecutionPrivateOutputAndOwnership(t *testing.T) {
	s, sc, req, tr := setupService(t)
	a, e := s.Preview(sc, req)
	if e != nil {
		t.Fatal(e)
	}
	req.ExpectedPlanHash = a.PlanHash
	x, e := s.Execute(context.Background(), sc, req, func(context.Context, Approval) (bool, error) { return true, nil })
	if e != nil || x.State != "completed" || tr.calls != 1 {
		t.Fatalf("%+v %v calls %d", x, e, tr.calls)
	}
	b, _ := json.Marshal(x)
	if strings.Contains(string(b), "SECRET") {
		t.Fatal("raw output leaked")
	}
	disk, _ := os.ReadFile(s.path)
	if strings.Contains(string(disk), "SECRET") || strings.Contains(string(disk), "printf hello") {
		t.Fatal("plaintext persisted")
	}
	aout, e := s.Artifact(sc, x.ArtifactID)
	if e != nil || aout.Stdout != "SECRET-CANARY" {
		t.Fatal(e)
	}
	if _, e = s.Artifact(Scope{"other", "ssh_exec"}, x.ArtifactID); e == nil {
		t.Fatal("cross owner artifact")
	}
	if _, e = s.Preview(Scope{"other", "ssh_exec"}, req); e == nil {
		t.Fatal("cross owner route")
	}
}
func TestApprovalMutationDisableAndDeny(t *testing.T) {
	for _, mode := range []string{"deny", "disable", "change", "credential"} {
		t.Run(mode, func(t *testing.T) {
			s, sc, req, tr := setupService(t)
			x, _ := s.Execute(context.Background(), sc, req, func(context.Context, Approval) (bool, error) {
				switch mode {
				case "deny":
					return false, nil
				case "disable":
					s.SetEnabled(false)
				case "change":
					s.mu.Lock()
					r := s.data.Routes[req.RouteID]
					r.Revision++
					s.data.Routes[r.ID] = r
					s.mu.Unlock()
				case "credential":
					s.mu.Lock()
					i := s.data.Identities[s.data.Routes[req.RouteID].Hops[0].IdentityID]
					s.mu.Unlock()
					s.vault.Set(context.Background(), credential.SetRequest{Name: i.CredentialRef, Type: domain.CredentialType("ssh_identity"), Scope: domain.CredentialScopeUser, OwnerID: sc.OwnerID, Value: `{"password":"new"}`})
				}
				return true, nil
			})
			if tr.calls != 0 || x.State != "cancelled" {
				t.Fatalf("dispatched changed consent: %+v", x)
			}
		})
	}
}
func TestDurableLockAndCrashRecovery(t *testing.T) {
	s, sc, req, _ := setupService(t)
	if _, e := NewService(s.path, s.vault, s.transport); e == nil {
		t.Fatal("second writer admitted")
	}
	s.mu.Lock()
	s.data.Executions["crashed"] = Execution{ID: "crashed", OwnerID: sc.OwnerID, RouteID: req.RouteID, State: "dispatch_started"}
	s.save()
	s.mu.Unlock()
	s.Close()
	again, e := NewService(s.path, s.vault, s.transport)
	if e != nil {
		t.Fatal(e)
	}
	defer again.Close()
	if again.Snapshot(sc).Executions["crashed"].State != "unknown_outcome" {
		t.Fatal("crashed command replayed")
	}
}
func TestUnknownOutcomeNoRetry(t *testing.T) {
	s, sc, req, tr := setupService(t)
	tr.run = func(context.Context) (TransportResult, error) {
		return TransportResult{Dispatched: true, ErrorCode: "disconnected"}, context.Canceled
	}
	x, e := s.Execute(context.Background(), sc, req, func(context.Context, Approval) (bool, error) { return true, nil })
	if e != nil || x.State != "unknown_outcome" || tr.calls != 1 {
		t.Fatalf("%+v %v", x, e)
	}
}

func TestPreviewConsumedOnceAndEpoch(t *testing.T) {
	s, sc, req, tr := setupService(t)
	a, e := s.Preview(sc, req)
	if e != nil {
		t.Fatal(e)
	}
	req.ApprovalID = a.ApprovalID
	req.ExpectedPlanHash = a.PlanHash
	yes := func(context.Context, Approval) (bool, error) { return true, nil }
	one, e := s.Execute(context.Background(), sc, req, yes)
	if e != nil {
		t.Fatal(e)
	}
	two, e := s.Execute(context.Background(), sc, req, yes)
	if e != nil || one.ID != two.ID || tr.calls != 1 {
		t.Fatalf("approval replay dispatched: %+v %v", two, e)
	}
	req.ApprovalID = ""
	req.ExpectedPlanHash = ""
	b, e := s.Preview(sc, req)
	if e != nil {
		t.Fatal(e)
	}
	s.SetEnabled(false)
	s.SetEnabled(true)
	req.ApprovalID = b.ApprovalID
	req.ExpectedPlanHash = b.PlanHash
	if _, e = s.Execute(context.Background(), sc, req, yes); e == nil {
		t.Fatal("old epoch approved")
	}
}
func TestSnapshotCannotMutateCatalog(t *testing.T) {
	s, sc, req, _ := setupService(t)
	snap := s.Snapshot(sc)
	r := snap.Routes[req.RouteID]
	r.Hops[0].IdentityID = "attacker"
	for _, v := range snap.Servers {
		v.HostKeys[0] = "attacker"
	}
	if _, e := s.Preview(sc, req); e != nil {
		t.Fatalf("snapshot changed catalog: %v", e)
	}
}

func TestCredentialRevokeCancelsActive(t *testing.T) {
	s, sc, req, tr := setupService(t)
	started := make(chan struct{})
	tr.run = func(ctx context.Context) (TransportResult, error) {
		close(started)
		<-ctx.Done()
		return TransportResult{Dispatched: true, ErrorCode: "cancelled"}, ctx.Err()
	}
	done := make(chan Execution, 1)
	go func() {
		x, _ := s.Execute(context.Background(), sc, req, func(context.Context, Approval) (bool, error) { return true, nil })
		done <- x
	}()
	<-started
	s.mu.Lock()
	i := s.data.Identities[s.data.Routes[req.RouteID].Hops[0].IdentityID]
	s.mu.Unlock()
	if e := s.CredentialChanged(i.CredentialRef); e != nil {
		t.Fatal(e)
	}
	select {
	case x := <-done:
		if x.State != "unknown_outcome" {
			t.Fatalf("revoke overclaimed termination: %+v", x)
		}
	case <-time.After(time.Second):
		t.Fatal("active credential not cancelled")
	}
}
func TestDedicatedCapabilitiesCannotExecuteArbitraryCommand(t *testing.T) {
	s, sc, req, tr := setupService(t)
	req.Capability = "key_install"
	if _, e := s.Execute(context.Background(), sc, req, func(context.Context, Approval) (bool, error) { return true, nil }); e == nil || tr.calls != 0 {
		t.Fatal("public key capability bypass")
	}
}

func TestCannotEnableWithoutEncryptionOrTransport(t *testing.T) {
	service, err := NewService(filepath.Join(t.TempDir(), "catalog.json"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { service.Close() })
	if err = service.SetEnabled(true); err == nil {
		t.Fatal("admitted without encrypted vault and transport")
	}
	if service.Snapshot(Scope{OwnerID: "owner", CallerID: "dashboard"}).Enabled {
		t.Fatal("reported enabled")
	}
}

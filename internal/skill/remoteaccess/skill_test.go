package remoteaccess

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/credential"
	"github.com/iulita-ai/iulita/internal/domain"
	engine "github.com/iulita-ai/iulita/internal/remoteaccess"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/skill/interact"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
	"go.uber.org/zap"
	"golang.org/x/crypto/ssh"
)

type fakeTransport struct {
	calls atomic.Int32
	run   func(context.Context) (engine.TransportResult, error)
}

func (f *fakeTransport) Run(ctx context.Context, _ engine.TransportRequest) (engine.TransportResult, error) {
	f.calls.Add(1)
	if f.run != nil {
		return f.run(ctx)
	}
	code := 0
	return engine.TransportResult{Dispatched: true, ExitCode: &code, Stdout: []byte("RAW-OUTPUT-CANARY")}, nil
}

type confirmer struct {
	actor string
	calls int
}

func (c *confirmer) Confirm(_ context.Context, a interact.Confirmation, expect interact.Actor) (interact.Decision, error) {
	c.calls++
	if a.Nonce == "" || a.Fingerprint == "" || !strings.Contains(a.Preview, "printf test") {
		return interact.Decision{}, errors.New("missing immutable preview")
	}
	return interact.Decision{Approved: true, Actor: interact.Actor{UserID: c.actor}}, nil
}
func fixture(t *testing.T, action string) (*engine.Service, *Skill, context.Context, json.RawMessage, *fakeTransport) {
	t.Helper()
	ctx := context.Background()
	db, e := sqlite.New(filepath.Join(t.TempDir(), "vault.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	if e = db.RunMigrations(ctx); e != nil {
		t.Fatal(e)
	}
	if e = db.CreateUser(ctx, &domain.User{ID: "owner", Username: "ssh-owner", Role: domain.RoleAdmin}); e != nil {
		t.Fatal(e)
	}
	crypt, e := config.NewEncryptor(make([]byte, 32))
	if e != nil {
		t.Fatal(e)
	}
	vault := credential.NewStore(credential.NewStorageAdapter(db), crypt, zap.NewNop())
	transport := &fakeTransport{}
	svc, e := engine.NewService(filepath.Join(t.TempDir(), "catalog.json"), vault, transport)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if err := svc.Close(); err != nil {
			t.Error(err)
		}
	})
	sc := engine.Scope{OwnerID: "owner", CallerID: "remote_" + action}
	pub, _, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	pk, e := ssh.NewPublicKey(pub)
	if e != nil {
		t.Fatal(e)
	}
	server, e := svc.PutServer(sc, engine.Server{Address: "127.0.0.1", Port: 22, HostKeys: []string{string(ssh.MarshalAuthorizedKey(pk))}, TrustSource: "test-console", Enabled: true})
	if e != nil {
		t.Fatal(e)
	}
	identity, e := svc.PutIdentity(ctx, sc, engine.Identity{Username: "deploy", AuthType: "password", Enabled: true}, &engine.Secret{Password: "CREDENTIAL-CANARY"}) // gitleaks:allow -- Synthetic fixture credential, never used with real servers.
	if e != nil {
		t.Fatal(e)
	}
	route, e := svc.PutRoute(sc, engine.Route{Hops: []engine.Hop{{ServerID: server.ID, IdentityID: identity.ID}}, Enabled: true})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = svc.PutGrant(sc, engine.Grant{CallerID: sc.CallerID, RouteID: route.ID, Capability: "exec", Enabled: true, ExpiresAt: time.Now().Add(time.Hour)}); e != nil {
		t.Fatal(e)
	}
	if e = svc.SetEnabled(true); e != nil {
		t.Fatal(e)
	}
	request, err := json.Marshal(engine.ExecuteRequest{RouteID: route.ID, Command: "printf test"})
	if err != nil {
		t.Fatal(err)
	}
	return svc, New(svc, action), skill.WithUserRole(skill.WithUserID(ctx, "owner"), "admin"), request, transport
}
func TestAdminCannotBypassSSHConfirmation(t *testing.T) {
	_, s, ctx, input, tr := fixture(t, "exec")
	out, _ := s.Execute(ctx, input)
	if tr.calls.Load() != 0 {
		t.Fatal("admin dispatched without confirmer")
	}
	if strings.Contains(out, "CANARY") {
		t.Fatal("secret leaked")
	}
}
func TestMismatchedConfirmationActorDenied(t *testing.T) {
	_, s, ctx, input, tr := fixture(t, "exec")
	c := &confirmer{actor: "other-user"}
	_, _ = s.Execute(interact.WithConfirmer(ctx, c), input)
	if tr.calls.Load() != 0 || c.calls != 1 {
		t.Fatalf("dispatch=%d confirmations=%d", tr.calls.Load(), c.calls)
	}
}
func TestChainStopsOnUnknownAndKeepsOutputPrivate(t *testing.T) {
	_, s, ctx, input, tr := fixture(t, "chain")
	tr.run = func(context.Context) (engine.TransportResult, error) {
		return engine.TransportResult{Dispatched: true, Stdout: []byte("RAW-OUTPUT-CANARY")}, errors.New("lost connection")
	}
	chain := json.RawMessage(`{"steps":[` + string(input) + `,` + string(input) + `]}`)
	out, err := s.Execute(interact.WithConfirmer(ctx, &confirmer{actor: "owner"}), chain)
	if err != nil {
		t.Fatal(err)
	}
	if tr.calls.Load() != 1 {
		t.Fatalf("chain continued after unknown: %d", tr.calls.Load())
	}
	if !strings.Contains(out, "unknown_outcome") || strings.Contains(out, "CANARY") {
		t.Fatalf("unsafe model result: %s", out)
	}
}
func TestSSHStrictArguments(t *testing.T) {
	_, s, ctx, input, tr := fixture(t, "exec")
	for _, bad := range []string{strings.TrimSuffix(string(input), "}") + `,"unexpected":true}`, strings.TrimSuffix(string(input), "}") + `,"approval_id":"forged"}`, strings.TrimSuffix(string(input), "}") + `,"capability":"key_install"}`, strings.TrimSuffix(string(input), "}") + `,"expected_plan_hash":"forged"}`, string(input) + ` {}`, `null`, `[]`} {
		_, err := s.Execute(ctx, json.RawMessage(bad))
		if err == nil {
			t.Errorf("accepted invalid input: %s", bad)
		}
	}
	if tr.calls.Load() != 0 {
		t.Fatal("malformed arguments dispatched")
	}
}
func TestRegistryDisableCancelsSSHAdmissionAndActiveConnection(t *testing.T) {
	svc, s, ctx, input, tr := fixture(t, "exec")
	started := make(chan struct{})
	tr.run = func(ctx context.Context) (engine.TransportResult, error) {
		close(started)
		<-ctx.Done()
		return engine.TransportResult{Dispatched: true}, ctx.Err()
	}
	registry := skill.NewRegistry()
	registry.RegisterWithManifest(s, Manifest())
	registry.EnableGroup("remote_access")
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = s.Execute(interact.WithConfirmer(ctx, &confirmer{actor: "owner"}), input)
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("execution not dispatched")
	}
	registry.DisableGroup("remote_access")
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("disable did not cancel active connection")
	}
	if svc.Snapshot(engine.Scope{OwnerID: "owner", CallerID: "remote_exec"}).Enabled {
		t.Fatal("service still admits operations")
	}
	_, _ = s.Execute(interact.WithConfirmer(ctx, &confirmer{actor: "owner"}), input)
	if tr.calls.Load() != 1 {
		t.Fatal("disabled service dispatched")
	}
}

func TestConfirmedExecutionReturnsMetadataOnly(t *testing.T) {
	_, s, ctx, input, tr := fixture(t, "exec")
	out, err := s.Execute(interact.WithConfirmer(ctx, &confirmer{actor: "owner"}), input)
	if err != nil || tr.calls.Load() != 1 {
		t.Fatalf("dispatch=%d error=%v", tr.calls.Load(), err)
	}
	if !strings.Contains(out, "artifact_id") || strings.Contains(out, "RAW-OUTPUT-CANARY") || strings.Contains(out, "CREDENTIAL-CANARY") || strings.Contains(out, "printf test") {
		t.Fatalf("unsafe model response %s", out)
	}
}
func TestMissingAuthenticatedOwnerCannotUseConfirmer(t *testing.T) {
	_, s, _, input, tr := fixture(t, "exec")
	_, err := s.Execute(interact.WithConfirmer(context.Background(), &confirmer{actor: "owner"}), input)
	if err == nil || tr.calls.Load() != 0 {
		t.Fatalf("unauthenticated dispatch=%d error=%v", tr.calls.Load(), err)
	}
}

func TestHiddenEngineFieldsDenied(t *testing.T) {
	_, s, ctx, input, tr := fixture(t, "exec")
	var req map[string]any
	if err := json.Unmarshal(input, &req); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"approval_id", "expected_plan_hash", "capability"} {
		req[field] = "hidden"
		b, _ := json.Marshal(req)
		if _, err := s.Execute(ctx, b); err == nil {
			t.Fatalf("hidden field %s accepted", field)
		}
		delete(req, field)
	}
	if tr.calls.Load() != 0 {
		t.Fatal("dispatched hidden field")
	}
}

type operationConfirmer struct{ actor string }

func (c operationConfirmer) Confirm(_ context.Context, a interact.Confirmation, _ interact.Actor) (interact.Decision, error) {
	if a.Fingerprint == "" || !strings.Contains(a.Preview, "SSH operation") {
		return interact.Decision{}, errors.New("missing typed preview")
	}
	return interact.Decision{Approved: true, Actor: interact.Actor{UserID: c.actor}}, nil
}
func TestTypedFilesToolPrivateAndMainOnly(t *testing.T) {
	svc, s, ctx, input, tr := fixture(t, "files")
	var original engine.ExecuteRequest
	if e := json.Unmarshal(input, &original); e != nil {
		t.Fatal(e)
	}
	sc := engine.Scope{OwnerID: "owner", CallerID: "remote_files"}
	if _, e := svc.PutGrant(sc, engine.Grant{RouteID: original.RouteID, CallerID: sc.CallerID, Capability: "file_get", FileRoot: "/data", Enabled: true, ExpiresAt: time.Now().Add(time.Hour)}); e != nil {
		t.Fatal(e)
	}
	raw, e := json.Marshal(map[string]string{"route_id": original.RouteID, "operation": "file_get", "root": "/data", "path": "private.txt"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Execute(ctx, raw); e == nil || tr.calls.Load() != 0 {
		t.Fatal("missing confirmer allowed")
	}
	if _, e = s.Execute(skill.WithRemoteCaller(ctx, "delegate"), raw); e == nil || tr.calls.Load() != 0 {
		t.Fatal("delegate allowed")
	}
	out, e := s.Execute(interact.WithConfirmer(ctx, operationConfirmer{actor: "owner"}), raw)
	if e != nil || tr.calls.Load() != 1 || strings.Contains(out, "CANARY") {
		t.Fatalf("%s %v calls%d", out, e, tr.calls.Load())
	}
	if !s.MainLoopOnly() {
		t.Fatal("file tool exposed unattended")
	}
}
func TestTypedBackgroundToolDoesNotAcceptExecCapability(t *testing.T) {
	_, s, ctx, input, tr := fixture(t, "background")
	var original engine.ExecuteRequest
	if e := json.Unmarshal(input, &original); e != nil {
		t.Fatal(e)
	}
	raw, e := json.Marshal(map[string]string{"route_id": original.RouteID, "operation": "background_start", "root": "/data", "job_id": "0123456789abcdef", "command": "printf test"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Execute(interact.WithConfirmer(ctx, operationConfirmer{actor: "owner"}), raw); e == nil || tr.calls.Load() != 0 {
		t.Fatal("exec grant authorized background capability")
	}
}
func TestEmptyPrivateFileArtifactCanBeUploaded(t *testing.T) {
	svc, s, ctx, input, tr := fixture(t, "files")
	var original engine.ExecuteRequest
	if e := json.Unmarshal(input, &original); e != nil {
		t.Fatal(e)
	}
	sc := engine.Scope{OwnerID: "owner", CallerID: "remote_files"}
	for _, capability := range []string{"file_get", "file_put"} {
		if _, e := svc.PutGrant(sc, engine.Grant{RouteID: original.RouteID, CallerID: sc.CallerID, Capability: capability, FileRoot: "/data", Enabled: true, ExpiresAt: time.Now().Add(time.Hour)}); e != nil {
			t.Fatal(e)
		}
	}
	tr.run = func(context.Context) (engine.TransportResult, error) {
		code := 0
		return engine.TransportResult{Dispatched: true, ExitCode: &code, File: &engine.FileResult{Path: "/data/empty", Size: 0, SHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}}, nil
	}
	call := func(body map[string]string) string {
		t.Helper()
		raw, e := json.Marshal(body)
		if e != nil {
			t.Fatal(e)
		}
		out, e := s.Execute(interact.WithConfirmer(ctx, operationConfirmer{actor: "owner"}), raw)
		if e != nil {
			t.Fatal(e)
		}
		return out
	}
	out := call(map[string]string{"route_id": original.RouteID, "operation": "file_get", "root": "/data", "path": "empty"})
	var x engine.Execution
	if e := json.Unmarshal([]byte(out), &x); e != nil {
		t.Fatal(e)
	}
	call(map[string]string{"route_id": original.RouteID, "operation": "file_put", "root": "/data", "path": "empty-copy", "artifact_id": x.ArtifactID})
	if tr.calls.Load() != 2 {
		t.Fatal("empty upload not dispatched")
	}
}

type privateOutputConfirmer struct {
	confirmer
	delivered   string
	owner       string
	deliveryErr error
	standing    string
}

func (c *privateOutputConfirmer) Confirm(ctx context.Context, a interact.Confirmation, actor interact.Actor) (interact.Decision, error) {
	if a.Reason != interact.ConfirmSSHCommand {
		return interact.Decision{}, errors.New("not SSH one-shot confirmation")
	}
	d, e := c.confirmer.Confirm(ctx, a, actor)
	d.Standing = c.standing
	return d, e
}
func (c *privateOutputConfirmer) DeliverPrivateResult(_ context.Context, actor interact.Actor, _, text string) error {
	c.owner = actor.UserID
	c.delivered = text
	return c.deliveryErr
}
func TestPrivateSSHOutputDeliveredOutOfBandAndNeverReturnedToModel(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "sent", true: "delivery failed"}[failure], func(t *testing.T) {
			_, s, ctx, input, tr := fixture(t, "exec")
			c := &privateOutputConfirmer{confirmer: confirmer{actor: "owner"}}
			if failure {
				c.deliveryErr = errors.New("channel unavailable")
			}
			out, err := s.Execute(interact.WithConfirmer(ctx, c), input)
			if err != nil {
				t.Fatal(err)
			}
			if c.owner != "owner" || !strings.Contains(c.delivered, "RAW-OUTPUT-CANARY") {
				t.Fatal("owner did not receive private output")
			}
			if strings.Contains(out, "CANARY") || strings.Contains(out, "stdout:") {
				t.Fatal("private bytes entered model result")
			}
			status := "sent_to_requesting_user"
			if failure {
				status = "available_in_remote_access"
			}
			if !strings.Contains(out, status) || tr.calls.Load() != 1 {
				t.Fatal("incorrect delivery metadata or SSH retry")
			}
		})
	}
}
func TestSSHStandingDecisionCannotAuthorizeCommand(t *testing.T) {
	_, s, ctx, input, tr := fixture(t, "exec")
	c := &privateOutputConfirmer{confirmer: confirmer{actor: "owner"}, standing: interact.StandingGlobal}
	_, _ = s.Execute(interact.WithConfirmer(ctx, c), input)
	if tr.calls.Load() != 0 || c.delivered != "" {
		t.Fatal("standing decision dispatched SSH or delivered output")
	}
}

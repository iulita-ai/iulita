package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/credential"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
	"go.uber.org/zap"
)

type fixtureProtocol struct {
	calls       atomic.Int32
	discoveries atomic.Int32
	invoked     chan struct{}
	release     chan struct{}
	changed     bool
}

func (f *fixtureProtocol) Discover(context.Context, Connection) ([]Item, error) {
	f.discoveries.Add(1)
	name := "fixture"
	if f.changed {
		name = "changed"
	}
	it, e := item("tool", map[string]any{"name": name, "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"message": map[string]any{"type": "string"}}, "required": []string{"message"}, "additionalProperties": false}})
	return []Item{it}, e
}

func TestDeploymentOriginsMustBeExactPublicHTTPSOrigins(t *testing.T) {
	f := fixture(t)
	for _, origin := range []string{"http://fixture.example", "https://fixture.example/", "https://fixture.example/path", "https://fixture.example?x=y", "https://FIXTURE.example", "https://localhost", "https://127.0.0.1", "https://fixture.example:8443"} {
		t.Run(origin, func(t *testing.T) {
			service, err := New(Options{Repository: f.s.repo, Vault: f.s.vault, Protocol: f.protocol, LockPath: filepath.Join(t.TempDir(), "origin.lock"), Enabled: true, AllowedOrigins: []string{origin}})
			if service != nil {
				if closeErr := service.Close(); closeErr != nil {
					t.Fatal(closeErr)
				}
			}
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid deployment origin accepted: %v", err)
			}
		})
	}
}

func TestRemoteOnlyRejectsLocalCreationUpdatesAndSavedConnections(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	snap, err := f.s.Snapshot(ctx, f.sc)
	if err != nil || snap.StdioEnabled {
		t.Fatal("local transport advertised by remote deployment", err)
	}
	draft := Draft{Label: "Local", Transport: "stdio", Profile: "fixture", Domain: "personal", ResultMode: "private"}
	if _, createErr := f.s.Create(ctx, f.sc, draft); !errors.Is(createErr, ErrUnavailable) {
		t.Fatal("local connection admitted", createErr)
	}
	c, err := f.s.Create(ctx, f.sc, Draft{Label: "Remote", Endpoint: "https://fixture.example/mcp", Domain: "personal"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Update(ctx, f.sc, c.ID, c.Revision, draft); !errors.Is(err, ErrUnavailable) {
		t.Fatal("local transport update admitted", err)
	}
	// Also fence a catalog persisted by an earlier opt-in broker deployment.
	f.s.mu.Lock()
	f.s.connections[c.ID].Transport = "stdio"
	f.s.connections[c.ID].Profile = "fixture"
	f.s.connections[c.ID].Desired = "enabled"
	f.s.connections[c.ID].Health = "ready"
	f.s.mu.Unlock()
	for _, action := range []string{"check", "connect", "reconnect", "refresh_catalog"} {
		if _, err := f.s.Action(ctx, f.sc, c.ID, action, c.Revision); !errors.Is(err, ErrUnavailable) {
			t.Fatal(action, err)
		}
	}
	if _, err := f.s.Prepare(ctx, f.sc, Call{ConnectionID: c.ID}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("saved local connection admitted an execution plan", err)
	}
	if f.protocol.discoveries.Load() != 0 || f.protocol.calls.Load() != 0 {
		t.Fatal("disabled local transport reached the protocol")
	}
}

func TestCancelledCallerCannotDispatchApprovedPlan(t *testing.T) {
	f := fixture(t)
	c, it := setup(t, f)
	p := prepare(t, f, c, it)
	if err := f.s.Approve(context.Background(), f.sc, p.ID, p.Hash, true); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.s.Execute(ctx, f.sc, p.ID); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if f.protocol.calls.Load() != 0 {
		t.Fatal("cancelled turn dispatched an external operation")
	}
}
func (f *fixtureProtocol) Invoke(ctx context.Context, _ Connection, _ Item, _ json.RawMessage, before func() error) (payload json.RawMessage, toolError bool, callErr error) {
	if e := before(); e != nil {
		return nil, false, e
	}
	f.calls.Add(1)
	if f.invoked != nil {
		close(f.invoked)
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}
	return json.RawMessage(`{"content":[{"type":"text","text":"RESULT-CANARY"}]}`), false, nil
}

type fixtureState struct {
	s        *Service
	repo     *sqlite.Store
	vault    *credential.Store
	protocol *fixtureProtocol
	path     string
	sc       Scope
}

func fixture(t *testing.T) fixtureState {
	t.Helper()
	dir := t.TempDir()
	db, e := sqlite.New(filepath.Join(dir, "fixture.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = db.Close() })
	if e = db.RunMigrations(context.Background()); e != nil {
		t.Fatal(e)
	}
	crypt, e := config.NewEncryptor(make([]byte, 32))
	if e != nil {
		t.Fatal(e)
	}
	vault := credential.NewStore(credential.NewStorageAdapter(db), crypt, zap.NewNop())
	f := &fixtureProtocol{}
	s, e := New(Options{Repository: db, Vault: vault, Protocol: f, LockPath: filepath.Join(dir, "mcp.lock"), Enabled: true, AllowedOrigins: []string{"https://fixture.example"}})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = s.Close() })
	return fixtureState{s, db, vault, f, dir, Scope{OwnerID: "owner", CallerID: "dashboard", Private: true}}
}
func setup(t *testing.T, f fixtureState) (Connection, Item) {
	t.Helper()
	c, e := f.s.Create(context.Background(), f.sc, Draft{Label: "Fixture", Endpoint: "https://fixture.example/mcp", Domain: "personal", ResultMode: "model"})
	if e != nil {
		t.Fatal(e)
	}
	c, e = f.s.Action(context.Background(), f.sc, c.ID, "connect", c.Revision)
	if e != nil || len(c.Items) != 1 {
		t.Fatal(c, e)
	}
	it := c.Items[0]
	if e = f.s.Review(context.Background(), f.sc, c.ID, c.Revision, Review{ItemID: it.ID, Revision: it.Revision, Effect: "write", Allowed: true}); e != nil {
		t.Fatal(e)
	}
	return c, it
}
func prepare(t *testing.T, f fixtureState, c Connection, it Item) Plan {
	t.Helper()
	p, e := f.s.Prepare(context.Background(), f.sc, Call{ConnectionID: c.ID, ItemID: it.ID, DescriptorRevision: it.Revision, Arguments: json.RawMessage(`{"message":"hello"}`)})
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func TestIsolationConsentAndReplay(t *testing.T) {
	f := fixture(t)
	c, it := setup(t, f)
	other := Scope{OwnerID: "other", CallerID: "dashboard", Private: true}
	if _, e := f.s.Connection(context.Background(), other, c.ID); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
	if items, e := f.s.Catalog(context.Background(), other, c.ID, ""); !errors.Is(e, ErrDenied) || len(items) > 0 {
		t.Fatal(e, items)
	}
	p := prepare(t, f, c, it)
	if _, e := f.s.Execute(context.Background(), f.sc, p.ID); !errors.Is(e, ErrApproval) {
		t.Fatal(e)
	}
	if e := f.s.Approve(context.Background(), other, p.ID, p.Hash, true); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	if e := f.s.Approve(context.Background(), f.sc, p.ID, p.Hash, true); e != nil {
		t.Fatal(e)
	}
	x, e := f.s.Execute(context.Background(), f.sc, p.ID)
	if e != nil || x.State != "succeeded" || x.Text != "RESULT-CANARY" {
		t.Fatal(x, e)
	}
	if _, e = f.s.Execute(context.Background(), f.sc, p.ID); e != nil || f.protocol.calls.Load() != 1 {
		t.Fatal(e, f.protocol.calls.Load())
	}
	if _, e = f.s.Artifact(context.Background(), other, x.ArtifactID); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
	if a, artifactErr := f.s.Artifact(context.Background(), f.sc, x.ArtifactID); artifactErr != nil || !strings.Contains(string(a.Payload), "RESULT-CANARY") {
		t.Fatal(a, artifactErr)
	}
	snap, e := f.s.Snapshot(context.Background(), f.sc)
	if e != nil || snap.Executions[0].Text != "" {
		t.Fatal(snap, e)
	}
}
func TestRevokeBeforeAndDuringDispatch(t *testing.T) {
	t.Run("pending", func(t *testing.T) {
		f := fixture(t)
		c, it := setup(t, f)
		p := prepare(t, f, c, it)
		_ = f.s.Approve(context.Background(), f.sc, p.ID, p.Hash, true)
		current, _ := f.s.Connection(context.Background(), f.sc, c.ID)
		if _, e := f.s.Action(context.Background(), f.sc, c.ID, "disconnect", current.Revision); e != nil {
			t.Fatal(e)
		}
		if _, e := f.s.Execute(context.Background(), f.sc, p.ID); e == nil || f.protocol.calls.Load() != 0 {
			t.Fatal(e)
		}
	})
	t.Run("sent", func(t *testing.T) {
		f := fixture(t)
		c, it := setup(t, f)
		p := prepare(t, f, c, it)
		_ = f.s.Approve(context.Background(), f.sc, p.ID, p.Hash, true)
		f.protocol.invoked = make(chan struct{})
		f.protocol.release = make(chan struct{})
		done := make(chan Execution)
		go func() { x, _ := f.s.Execute(context.Background(), f.sc, p.ID); done <- x }()
		<-f.protocol.invoked
		current, _ := f.s.Connection(context.Background(), f.sc, c.ID)
		if _, e := f.s.Action(context.Background(), f.sc, c.ID, "disconnect", current.Revision); e != nil {
			t.Fatal(e)
		}
		x := <-done
		if x.State != "unknown_outcome" || x.Text != "" || f.protocol.calls.Load() != 1 {
			t.Fatal(x)
		}
	})
}
func TestEncryptedSecretsAndDurableDisable(t *testing.T) {
	f := fixture(t)
	c, _ := setup(t, f)
	c, _ = f.s.Connection(context.Background(), f.sc, c.ID)
	if e := f.s.SetCredentials(context.Background(), f.sc, c.ID, c.Revision, "bearer", "TOKEN-SECRET-CANARY", nil); e != nil {
		t.Fatal(e)
	}
	rows, e := f.repo.LoadMCPRecords(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range rows {
		if strings.Contains(r.Payload, "TOKEN-SECRET-CANARY") || strings.Contains(r.Payload, "fixture.example") {
			t.Fatal("plaintext persisted")
		}
	}
	raw, e := os.ReadFile(filepath.Join(f.path, "fixture.db"))
	if e != nil || strings.Contains(string(raw), "TOKEN-SECRET-CANARY") {
		t.Fatal(e)
	}
	if e = f.s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e := New(Options{Repository: f.repo, Vault: f.vault, Protocol: f.protocol, LockPath: filepath.Join(f.path, "mcp.lock"), Enabled: true, AllowedOrigins: []string{"https://fixture.example"}})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	v, e := s.Connection(context.Background(), f.sc, c.ID)
	if e != nil || v.Desired != "disabled" {
		t.Fatal(v, e)
	}
	b, _ := json.Marshal(v)
	if strings.Contains(string(b), "TOKEN-SECRET-CANARY") {
		t.Fatal("secret exposed")
	}
}
func TestSchemasAndCatalogRevisions(t *testing.T) {
	f := fixture(t)
	c, it := setup(t, f)
	for _, args := range []string{`{"message":1}`, `{"message":"a","message":"b"}`, `{"message":"a","secret":"x"}`} {
		if _, e := f.s.Prepare(context.Background(), f.sc, Call{ConnectionID: c.ID, ItemID: it.ID, DescriptorRevision: it.Revision, Arguments: json.RawMessage(args)}); e == nil {
			t.Fatal(args)
		}
	}
	p := prepare(t, f, c, it)
	f.protocol.changed = true
	c, _ = f.s.Connection(context.Background(), f.sc, c.ID)
	next, e := f.s.Action(context.Background(), f.sc, c.ID, "refresh_catalog", c.Revision)
	if e != nil || next.Items[0].Allowed {
		t.Fatal(next, e)
	}
	if e = f.s.Approve(context.Background(), f.sc, p.ID, p.Hash, true); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
}
func TestSingleWriterAndScope(t *testing.T) {
	f := fixture(t)
	if _, e := New(Options{Repository: f.repo, Vault: f.vault, Protocol: f.protocol, LockPath: filepath.Join(f.path, "mcp.lock")}); e == nil {
		t.Fatal("second writer admitted")
	}
	for _, sc := range []Scope{{OwnerID: "owner", CallerID: "delegate", Private: true}, {OwnerID: "owner", CallerID: "main", Private: false}, {CallerID: "dashboard", Private: true}} {
		if _, e := f.s.Snapshot(context.Background(), sc); !errors.Is(e, ErrDenied) {
			t.Fatal(sc, e)
		}
	}
}

func TestPlanConversationBindingAndAdmission(t *testing.T) {
	f := fixture(t)
	c, it := setup(t, f)
	f.sc = Scope{OwnerID: "owner", CallerID: "main", ConversationID: "private-a", Private: true}
	p := prepare(t, f, c, it)
	other := f.sc
	other.ConversationID = "private-b"
	if err := f.s.Approve(context.Background(), other, p.ID, p.Hash, true); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err := f.s.Approve(context.Background(), f.sc, p.ID, p.Hash, true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Execute(context.Background(), other, p.ID); !errors.Is(err, ErrApproval) {
		t.Fatal(err)
	}
	if err := f.s.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Start(context.Background(), f.sc, p.ID); err == nil {
		t.Fatal("acknowledged rejected execution")
	}
	if f.protocol.calls.Load() != 0 {
		t.Fatal("dispatched rejected execution")
	}
}

func TestIdleConnectionAndCatalogRefreshStayDisabled(t *testing.T) {
	f := fixture(t)
	c, it := setup(t, f)
	f.s.mu.Lock()
	f.s.connections[c.ID].CheckedAt = time.Now().Add(-time.Hour)
	f.s.mu.Unlock()
	_ = prepare(t, f, c, it)
	c, _ = f.s.Connection(context.Background(), f.sc, c.ID)
	c, err := f.s.Action(context.Background(), f.sc, c.ID, "disconnect", c.Revision)
	if err != nil {
		t.Fatal(err)
	}
	c, err = f.s.Action(context.Background(), f.sc, c.ID, "refresh_catalog", c.Revision)
	if err != nil || c.Desired != "disabled" {
		t.Fatal(c, err)
	}
}

func TestClosedServiceCannotMutateReleasedStore(t *testing.T) {
	f := fixture(t)
	if e := f.s.Close(); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.Create(context.Background(), f.sc, Draft{Label: "late", Endpoint: "https://fixture.example/mcp", Domain: "personal"}); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
	if e := f.s.SetEnabled(true); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
}

func TestArtifactQuotaDoesNotEraseKnownProviderOutcome(t *testing.T) {
	f := fixture(t)
	c, it := setup(t, f)
	p := prepare(t, f, c, it)
	if e := f.s.Approve(context.Background(), f.sc, p.ID, p.Hash, true); e != nil {
		t.Fatal(e)
	}
	f.s.mu.Lock()
	f.s.artifacts["quota-fixture"] = artifactInfo{owner: f.sc.OwnerID, bytes: 100 << 20, expires: time.Now().Add(time.Hour)}
	f.s.mu.Unlock()
	result, e := f.s.Execute(context.Background(), f.sc, p.ID)
	if e != nil || result.State != "result_unavailable" || result.SideEffectState != "provider_reported_complete" || result.ArtifactID != "" {
		t.Fatal(result, e)
	}
	if _, e = f.s.Execute(context.Background(), f.sc, p.ID); e != nil || f.protocol.calls.Load() != 1 {
		t.Fatal("storage failure caused replay", e)
	}
}

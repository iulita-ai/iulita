package modelruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/models"
)

func seedOperationConnection(t *testing.T, m *Manager, key string) {
	t.Helper()
	if err := m.SeedConnections(context.Background(), []Connection{{Provider: "zai", APIKey: key, Source: "legacy_effective"}}); err != nil {
		t.Fatal(err)
	}
}

func TestOperationConnectionUsesActiveHandleOutsideLock(t *testing.T) {
	m := newTestManager(t, &memoryRepo{}, echoFactory)
	seedOperationConnection(t, m, "operation-fixture-key")
	// A candidate key must never become available before activation.
	if _, err := m.Stage(context.Background(), "admin", 0, models.Settings{SchemaVersion: 1}, []ConnectionMutation{{Provider: "zai", APIKeyAction: "replace", APIKey: "staged"}}); err != nil {
		t.Fatal(err)
	}
	callbackErr := errors.New("callback outcome")
	done := make(chan error, 1)
	go func() {
		done <- m.WithOperationConnection(context.Background(), "zai", "translation", func(ctx context.Context, c Connection, revision uint64) error {
			// These calls acquire the same lock; they must work inside callback.
			if m.OperationAvailability("zai") != "ready" {
				return errors.New("callback could not inspect runtime")
			}
			_, _, _, _ = m.Bindings()
			if c.APIKey != "operation-fixture-key" || c.Generation == "" || c.Endpoint != "https://api.z.ai/api/paas/v4" || !c.Enabled || revision != 0 || ctx.Err() != nil {
				return errors.New("callback received an incorrect active snapshot")
			}
			return callbackErr
		})
	}()
	select {
	case err := <-done:
		if !errors.Is(err, callbackErr) {
			t.Fatalf("callback error was not preserved: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("callback ran while holding the manager lock")
	}
	m.mu.Lock()
	active := len(m.admissions)
	m.mu.Unlock()
	if active != 0 {
		t.Fatal("callback error leaked admission")
	}
}

func TestOperationConnectionAvailabilityAndRejections(t *testing.T) {
	var absent *Manager
	if absent.OperationAvailability("zai") != "runtime_unavailable" {
		t.Fatal("nil runtime reported ready")
	}
	noop := func(context.Context, Connection, uint64) error { return nil }
	assertCode(t, absent.WithOperationConnection(context.Background(), "zai", "translation", noop), "runtime_unavailable")
	withoutCipher, err := New(&memoryRepo{}, nil, echoFactory, nil)
	if err != nil {
		t.Fatal(err)
	}
	if withoutCipher.OperationAvailability("zai") != "runtime_unavailable" {
		t.Fatal("unprotected runtime reported ready")
	}
	assertCode(t, withoutCipher.WithOperationConnection(context.Background(), "zai", "translation", noop), "runtime_unavailable")

	m := newTestManager(t, &memoryRepo{}, echoFactory)
	if m.OperationAvailability("zai") != "not_configured" {
		t.Fatal("missing connection reported ready")
	}
	assertCode(t, m.WithOperationConnection(context.Background(), "zai", "translation", noop), "credential_required")
	seedOperationConnection(t, m, "operation-fixture-key")
	called := false
	callback := func(context.Context, Connection, uint64) error { called = true; return nil }
	for _, pair := range [][2]string{{"openai", "translation"}, {"zai", "chat"}, {"zai", ""}, {"ZAI", "translation"}} {
		assertCode(t, m.WithOperationConnection(context.Background(), pair[0], pair[1], callback), "unsupported_operation")
	}
	assertCode(t, m.WithOperationConnection(context.Background(), "zai", "translation", nil), "invalid_operation_callback")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.WithOperationConnection(canceled, "zai", "translation", callback); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled caller was admitted")
	}
	if called {
		t.Fatal("invalid operation reached callback")
	}
	if m.OperationAvailability("openai") != "not_configured" {
		t.Fatal("unsupported provider reported operation readiness")
	}

	// Unusual active metadata still fails closed without handing out its key.
	for _, fixture := range []struct {
		name, expected, code string
		mutate               func(*Manager)
	}{
		{"disabled", "revoked", "credential_revoked", func(m *Manager) {
			c := m.state.Connections["zai"]
			c.Connection.Enabled = false
			m.state.Connections["zai"] = c
		}},
		{"denied-generation", "revoked", "credential_revoked", func(m *Manager) { m.state.Denied[m.state.Connections["zai"].Connection.Generation] = m.now() }},
		{"denied-identity", "revoked", "credential_revoked", func(m *Manager) {
			c := m.state.Connections["zai"]
			m.state.DeniedCredentials[secretIdentity("zai", c.Secret, m.state.IdentitySalt)] = m.now()
		}},
		{"empty-secret", "not_configured", "credential_required", func(m *Manager) { c := m.state.Connections["zai"]; c.Secret = ""; m.state.Connections["zai"] = c }},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			current := newTestManager(t, &memoryRepo{}, echoFactory)
			seedOperationConnection(t, current, "operation-fixture-key")
			current.mu.Lock()
			fixture.mutate(current)
			current.mu.Unlock()
			if current.OperationAvailability("zai") != fixture.expected {
				t.Fatal("incorrect unavailable state")
			}
			assertCode(t, current.WithOperationConnection(context.Background(), "zai", "translation", callback), fixture.code)
		})
	}
	if called {
		t.Fatal("unavailable operation reached callback")
	}
}

func TestOperationConnectionMutationCancelsAndReadsCurrentGeneration(t *testing.T) {
	for _, mutation := range []string{"rotate", "revoke", "forbid", "close"} {
		t.Run(mutation, func(t *testing.T) {
			m := newTestManager(t, &memoryRepo{}, echoFactory)
			seedOperationConnection(t, m, "operation-fixture-key")
			activateDraft(t, m, stageDraft(t, m, 0, "conversation-fixture-key"))
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			entered := make(chan Connection, 1)
			done := make(chan error, 1)
			go func() {
				done <- m.WithOperationConnection(ctx, "zai", "translation", func(ctx context.Context, c Connection, revision uint64) error {
					if revision != 1 {
						return errors.New("incorrect captured revision")
					}
					entered <- c
					<-ctx.Done()
					// A late successful callback cannot override revoked/shutdown state.
					return nil
				})
			}()
			var initial Connection
			select {
			case initial = <-entered:
			case <-ctx.Done():
				t.Fatal("operation was not admitted")
			}
			wantAvailability, wantCode := "revoked", "credential_revoked"
			switch mutation {
			case "rotate", "forbid":
				settings := draftSettings()
				var changes []ConnectionMutation
				if mutation == "rotate" {
					changes = []ConnectionMutation{{Provider: "zai", APIKeyAction: "replace", APIKey: "nextkey"}}
					wantAvailability = "ready"
				} else {
					settings.Policy.ForbiddenProviders = []string{"zai"}
					wantAvailability, wantCode = "forbidden", "forbidden_provider"
				}
				stage, err := m.Stage(ctx, "admin", 1, settings, changes)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = m.Activate(ctx, "admin", 1, stage.ID); err != nil {
					t.Fatal(err)
				}
			case "revoke":
				if _, err := m.Revoke(ctx, "admin", 1, "zai", initial.Generation); err != nil {
					t.Fatal(err)
				}
			case "close":
				if err := m.Close(ctx); err != nil {
					t.Fatal(err)
				}
				wantAvailability, wantCode = "runtime_unavailable", "runtime_unavailable"
			}
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("in-flight attempt was not canceled: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("runtime mutation did not cancel operation")
			}
			if m.OperationAvailability("zai") != wantAvailability {
				t.Fatal("availability did not reflect active mutation")
			}
			called := false
			err := m.WithOperationConnection(ctx, "zai", "translation", func(_ context.Context, c Connection, revision uint64) error {
				called = true
				if c.Generation == initial.Generation || c.APIKey != "nextkey" || revision != 2 {
					return errors.New("new attempt used stale connection metadata")
				}
				return nil
			})
			if mutation == "rotate" {
				if err != nil || !called {
					t.Fatalf("replacement connection not used: %v", err)
				}
			} else {
				assertCode(t, err, wantCode)
				if called {
					t.Fatal("denied operation reached callback")
				}
			}
			if err := m.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := m.Wait(ctx); err != nil {
				t.Fatalf("operation admission was not released: %v", err)
			}
		})
	}
}

func TestOperationConnectionParentCancellationAndPanicRelease(t *testing.T) {
	m := newTestManager(t, &memoryRepo{}, echoFactory)
	seedOperationConnection(t, m, "operation-fixture-key")
	ctx, cancel := context.WithCancel(context.Background())
	err := m.WithOperationConnection(ctx, "zai", "translation", func(admitted context.Context, _ Connection, _ uint64) error {
		cancel()
		<-admitted.Done()
		return admitted.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("parent cancellation was detached")
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("callback panic did not propagate")
			}
		}()
		_ = m.WithOperationConnection(context.Background(), "zai", "translation", func(context.Context, Connection, uint64) error {
			panic("fixture panic")
		})
	}()
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	wait, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := m.Wait(wait); err != nil {
		t.Fatalf("cancellation or panic leaked admission: %v", err)
	}
}

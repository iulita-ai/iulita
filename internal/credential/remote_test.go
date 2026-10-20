package credential

import (
	"context"
	"testing"

	"github.com/iulita-ai/iulita/internal/domain"
	"go.uber.org/zap"
)

func TestSSHIdentityOnlyStrictResolver(t *testing.T) {
	ctx := context.Background()
	r := newMockRepo()
	s := NewStore(r, &mockEncryptor{enabled: true}, zap.NewNop())
	for _, name := range []string{"ssh.identity.id", "unusual-name"} {
		c, e := s.Set(ctx, SetRequest{Name: name, Type: domain.CredentialType("ssh_identity"), Scope: domain.CredentialScopeUser, OwnerID: "alice", Value: `{"password":"CANARY"}`})
		if e != nil {
			t.Fatal(e)
		}
		if v, resolveErr := s.ResolvePrivate(ctx, "alice", name, "ssh_identity"); resolveErr != nil || v == "" {
			t.Fatalf("strict resolver unavailable: %v", resolveErr)
		}
		if _, e = s.ResolvePrivate(ctx, "bob", name, "ssh_identity"); e == nil {
			t.Fatal("cross owner")
		}
		if _, e = s.ResolveForUser(ctx, name, "alice"); e == nil {
			t.Fatal("generic owner resolver leaked")
		}
		if _, ok := s.decrypt(name, c); ok {
			t.Fatal("generic decrypt leaked")
		}
		if e = s.Bind(ctx, c.ID, domain.CredentialConsumerConfigKey, "foo", "alice"); e == nil {
			t.Fatal("binding allowed")
		}
		r.bindings = append(r.bindings, domain.CredentialBinding{CredentialID: c.ID, ConsumerType: "test", ConsumerID: name})
		if _, e = s.ResolveForConsumer(ctx, "test", name); e == nil {
			t.Fatal("existing binding leaked")
		}
	}
}
func TestSSHNamespaceCannotFallBack(t *testing.T) {
	s := NewStore(newMockRepo(), nil, zap.NewNop())
	s.RegisterEnvMapping("ssh.identity.id", "SSH_TEST_CANARY")
	t.Setenv("SSH_TEST_CANARY", "CANARY")
	if _, e := s.Resolve(context.Background(), "ssh.identity.id"); e == nil {
		t.Fatal("ambient fallback allowed")
	}
}

package external

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
)

type exactSender struct {
	calls          int
	instance, chat string
}

func (s *exactSender) SendToInstance(_ context.Context, instance, chat, _ string) error {
	s.calls++
	s.instance, s.chat = instance, chat
	return nil
}

func TestCanonicalPrivateBindingRevalidationAndExactSend(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(filepath.Join(t.TempDir(), "canonical.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err = store.RunMigrations(ctx); err != nil {
		t.Fatal(err)
	}
	if err = store.CreateUser(ctx, &domain.User{ID: "owner", Username: "owner", PasswordHash: "test-only", Role: domain.RoleRegular}); err != nil {
		t.Fatal(err)
	}
	config := &domain.ChannelInstance{ID: "tg-private", Type: domain.ChannelTypeTelegram, Name: "private", Source: domain.ChannelSourceDashboard, Config: "{}", Enabled: true}
	if err = store.CreateChannelInstance(ctx, config); err != nil {
		t.Fatal(err)
	}
	binding := &domain.UserChannel{UserID: "owner", ChannelInstanceID: config.ID, ChannelType: config.Type, ChannelID: "123", ChannelUserID: "123", Enabled: true}
	if err = store.BindChannel(ctx, binding); err != nil {
		t.Fatal(err)
	}
	sender := new(exactSender)
	adapter := Adapter{Store: store, Sender: sender}
	origin, err := adapter.ResolvePrivate(ctx, "owner", config.ID, "123")
	if err != nil {
		t.Fatal(err)
	}
	if err = adapter.SendTemplate(ctx, origin, "fixed metadata"); err != nil || sender.calls != 1 || sender.instance != config.ID || sender.chat != "123" {
		t.Fatal("exact private send", err)
	}
	for _, target := range []struct{ owner, instance, chat string }{{"foreign", config.ID, "123"}, {"owner", "missing", "123"}, {"owner", config.ID, "-123"}} {
		if _, err = adapter.ResolvePrivate(ctx, target.owner, target.instance, target.chat); err == nil {
			t.Fatal("untrusted private target accepted", target)
		}
	}
	config.Config = `{"changed":true}`
	if err = store.UpdateChannelInstance(ctx, config); err != nil {
		t.Fatal(err)
	}
	if err = adapter.SendTemplate(ctx, origin, "stale metadata"); err == nil || sender.calls != 1 {
		t.Fatal("changed binding still routed")
	}
	config.Enabled = false
	if err = store.UpdateChannelInstance(ctx, config); err != nil {
		t.Fatal(err)
	}
	if _, err = adapter.ResolvePrivate(ctx, "owner", config.ID, "123"); err == nil {
		t.Fatal("disabled instance accepted")
	}
	config.Enabled = true
	if err = store.UpdateChannelInstance(ctx, config); err != nil {
		t.Fatal(err)
	}
	if err = store.UnbindChannel(ctx, binding.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = adapter.ResolvePrivate(ctx, "owner", config.ID, "123"); err == nil {
		t.Fatal("unbound private origin accepted")
	}
}

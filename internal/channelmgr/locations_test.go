package channelmgr

import (
	"context"
	"errors"
	"testing"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/channel/telegram"
	"github.com/iulita-ai/iulita/internal/channel/webchat"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	store, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	if err := store.RunMigrations(context.Background()); err != nil {
		t.Fatalf("running migrations: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return New(Config{Store: store, Logger: zap.NewNop()})
}

func TestPickTelegramDeterministic(t *testing.T) {
	tgA := &telegram.Channel{}
	tgB := &telegram.Channel{}
	tgC := &telegram.Channel{}
	running := map[string]*ManagedChannel{
		"tg-c":  {tg: tgC},
		"tg-a":  {tg: tgA},
		"tg-b":  {tg: tgB},
		"web-1": {web: &webchat.Channel{}},
	}
	// Deterministic: lowest instance ID wins, across any map iteration order.
	for range 10 {
		if got, count := pickTelegram(running); got != tgA || count != 3 {
			t.Fatalf("pickTelegram not deterministic: got tg-a? %v, count %d", got == tgA, count)
		}
	}
	if got, count := pickTelegram(map[string]*ManagedChannel{"web-1": {web: &webchat.Channel{}}}); got != nil || count != 0 {
		t.Fatalf("pickTelegram with no telegram = %v (count %d), want nil", got, count)
	}
}

func TestLocationTargetFor(t *testing.T) {
	t.Run("non-numeric chat ids are unsupported", func(t *testing.T) {
		m := newTestManager(t)
		for _, chatID := range []string{"console", "web:abc", "slack:C123", ""} {
			if _, _, err := m.locationTargetFor(context.Background(), chatID); !errors.Is(err, channel.ErrLocationUnsupported) {
				t.Fatalf("chatID %q: err = %v, want ErrLocationUnsupported", chatID, err)
			}
		}
	})

	t.Run("numeric chat, no binding, no telegram running", func(t *testing.T) {
		m := newTestManager(t)
		if _, _, err := m.locationTargetFor(context.Background(), "12345"); !errors.Is(err, channel.ErrLocationUnsupported) {
			t.Fatalf("err = %v, want ErrLocationUnsupported", err)
		}
	})

	t.Run("numeric chat, no binding, single telegram fallback", func(t *testing.T) {
		m := newTestManager(t)
		tgA := &telegram.Channel{}
		m.mu.Lock()
		m.running["tg-a"] = &ManagedChannel{tg: tgA}
		m.mu.Unlock()

		tg, chatID, err := m.locationTargetFor(context.Background(), "12345")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if tg != tgA {
			t.Fatal("fallback did not pick the single telegram instance")
		}
		if chatID != 12345 {
			t.Fatalf("chatID = %d, want 12345", chatID)
		}
	})

	t.Run("numeric chat, no binding, TWO telegrams running: ambiguous, unsupported", func(t *testing.T) {
		m := newTestManager(t)
		m.mu.Lock()
		m.running["tg-a"] = &ManagedChannel{tg: &telegram.Channel{}}
		m.running["tg-b"] = &ManagedChannel{tg: &telegram.Channel{}}
		m.mu.Unlock()

		// The DB instance column is never populated today; with 2+ bots the
		// fallback target is ambiguous (wrong-bot send would deliver the pin
		// into a different conversation) → refuse, skill degrades to a link.
		if _, _, err := m.locationTargetFor(context.Background(), "12345"); !errors.Is(err, channel.ErrLocationUnsupported) {
			t.Fatalf("err = %v, want ErrLocationUnsupported on ambiguous multi-bot fallback", err)
		}
	})

	t.Run("db-bound web chat is unsupported", func(t *testing.T) {
		m := newTestManager(t)
		// Create a user + channel binding that maps chat "999" to a web instance.
		u := &domain.User{Username: "loc-test", PasswordHash: "x", Role: "user"}
		if err := m.store.CreateUser(context.Background(), u); err != nil {
			t.Fatalf("creating user: %v", err)
		}
		binding := &domain.UserChannel{
			UserID: u.ID, ChannelType: domain.ChannelTypeWeb,
			ChannelID: "999", ChannelUserID: "999", ChannelInstanceID: "web-1",
		}
		if err := m.store.BindChannel(context.Background(), binding); err != nil {
			t.Fatalf("binding channel: %v", err)
		}
		m.mu.Lock()
		m.running["web-1"] = &ManagedChannel{web: &webchat.Channel{}}
		m.mu.Unlock()

		if _, _, err := m.locationTargetFor(context.Background(), "999"); !errors.Is(err, channel.ErrLocationUnsupported) {
			t.Fatalf("err = %v, want ErrLocationUnsupported for web-bound chat", err)
		}
	})

	t.Run("db-bound telegram chat routes to that instance", func(t *testing.T) {
		m := newTestManager(t)
		u := &domain.User{Username: "loc-test-2", PasswordHash: "x", Role: "user"}
		if err := m.store.CreateUser(context.Background(), u); err != nil {
			t.Fatalf("creating user: %v", err)
		}
		binding := &domain.UserChannel{
			UserID: u.ID, ChannelType: domain.ChannelTypeTelegram,
			ChannelID: "777", ChannelUserID: "777", ChannelInstanceID: "tg-bound",
		}
		if err := m.store.BindChannel(context.Background(), binding); err != nil {
			t.Fatalf("binding channel: %v", err)
		}
		tgBound := &telegram.Channel{}
		tgOther := &telegram.Channel{}
		m.mu.Lock()
		m.running["tg-bound"] = &ManagedChannel{tg: tgBound}
		m.running["tg-a"] = &ManagedChannel{tg: tgOther}
		m.mu.Unlock()

		tg, _, err := m.locationTargetFor(context.Background(), "777")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if tg != tgBound {
			t.Fatal("db-bound chat did not route to its own instance")
		}
	})
}

func TestCanSendLocations(t *testing.T) {
	m := newTestManager(t)
	if m.CanSendLocations("console") {
		t.Fatal("console chat must not support locations")
	}
	if m.CanSendLocations("12345") {
		t.Fatal("no telegram running → must be false")
	}
	m.mu.Lock()
	m.running["tg-a"] = &ManagedChannel{tg: &telegram.Channel{}}
	m.mu.Unlock()
	if !m.CanSendLocations("12345") {
		t.Fatal("telegram running + numeric chat → must be true")
	}
}

func TestLocationTargetForBoundStoppedInstance(t *testing.T) {
	m := newTestManager(t)
	u := &domain.User{Username: "loc-stopped", PasswordHash: "x", Role: "user"}
	if err := m.store.CreateUser(context.Background(), u); err != nil {
		t.Fatalf("creating user: %v", err)
	}
	binding := &domain.UserChannel{
		UserID: u.ID, ChannelType: domain.ChannelTypeTelegram,
		ChannelID: "555", ChannelUserID: "555", ChannelInstanceID: "tg-bound",
	}
	if err := m.store.BindChannel(context.Background(), binding); err != nil {
		t.Fatalf("binding channel: %v", err)
	}
	// The bound instance is NOT running; another Telegram bot is. The send
	// must fail closed — never route the pin through the other bot.
	m.mu.Lock()
	m.running["tg-a"] = &ManagedChannel{tg: &telegram.Channel{}}
	m.mu.Unlock()

	if _, _, err := m.locationTargetFor(context.Background(), "555"); !errors.Is(err, channel.ErrLocationUnsupported) {
		t.Fatalf("err = %v, want ErrLocationUnsupported (fail closed on stopped bound instance)", err)
	}
}

// TestLocationTargetForDBLookupErrorFailsClosed: a DB failure during the
// binding lookup must fail CLOSED — treating it as "unbound" would re-enable
// the single-running-bot fallback and could deliver the pin via the wrong bot.
func TestLocationTargetForDBLookupErrorFailsClosed(t *testing.T) {
	m := newTestManager(t)
	u := &domain.User{Username: "loc-dberr", PasswordHash: "x", Role: "user"}
	if err := m.store.CreateUser(context.Background(), u); err != nil {
		t.Fatalf("creating user: %v", err)
	}
	binding := &domain.UserChannel{
		UserID: u.ID, ChannelType: domain.ChannelTypeTelegram,
		ChannelID: "444", ChannelUserID: "444", ChannelInstanceID: "tg-bound",
	}
	if err := m.store.BindChannel(context.Background(), binding); err != nil {
		t.Fatalf("binding channel: %v", err)
	}
	m.mu.Lock()
	m.running["tg-a"] = &ManagedChannel{tg: &telegram.Channel{}}
	m.mu.Unlock()

	// Force a lookup failure by closing the store.
	if err := m.store.Close(); err != nil {
		t.Fatalf("closing store: %v", err)
	}

	if _, _, err := m.locationTargetFor(context.Background(), "444"); !errors.Is(err, channel.ErrLocationUnsupported) {
		t.Fatalf("err = %v, want ErrLocationUnsupported on DB lookup failure", err)
	}
}

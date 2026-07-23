package slack

import (
	"context"
	"errors"
	"testing"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage"
)

// fakeRouteStore is a minimal storage.Repository that only implements the route
// lookup; the embedded nil interface panics on any other call, which keeps the
// fake honest (a test that trips an unexpected store call fails loudly).
type fakeRouteStore struct {
	storage.Repository
	route *domain.SlackChatRoute
	err   error
}

func (f *fakeRouteStore) GetSlackRoute(_ context.Context, _, _ string) (*domain.SlackChatRoute, error) {
	return f.route, f.err
}

func TestParseChannelID(t *testing.T) {
	tests := []struct {
		name   string
		chatID string
		want   string
	}{
		{"dm", "slack:D1234567890", "D1234567890"},
		{"channel", "slack:C1234567890:U9876543210", "C1234567890"},
		{"private group", "slack:G1234:U5678", "G1234"},
		{"not slack", "telegram:12345", ""},
		{"bare id", "D1234", ""},
		{"empty", "", ""},
		{"prefix only", "slack:", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseChannelID(tt.chatID); got != tt.want {
				t.Errorf("parseChannelID(%q) = %q, want %q", tt.chatID, got, tt.want)
			}
		})
	}
}

// resolveMeta must recover a routable channel from the chatID even with an empty
// cache and no store — this is the fix that makes proactive delivery survive
// eviction/restart instead of returning "no chat context".
func TestResolveMeta_ParseFallback(t *testing.T) {
	c := &Channel{
		chatMetaM: make(map[string]*chatMeta),
		logger:    zap.NewNop(),
		// store intentionally nil
	}

	meta := c.resolveMeta(context.Background(), "slack:D999")
	if meta == nil {
		t.Fatal("expected non-nil meta from chatID parse fallback")
	}
	if meta.channelID != "D999" {
		t.Errorf("channelID = %q, want D999", meta.channelID)
	}
	if meta.threadTS != "" {
		t.Errorf("threadTS = %q, want empty (unrecoverable from chatID)", meta.threadTS)
	}
}

func TestResolveMeta_ChannelFallbackDropsUser(t *testing.T) {
	c := &Channel{chatMetaM: make(map[string]*chatMeta), logger: zap.NewNop()}
	meta := c.resolveMeta(context.Background(), "slack:C123:U456")
	if meta == nil || meta.channelID != "C123" {
		t.Fatalf("channelID = %+v, want C123", meta)
	}
}

// A cache hit must win over the fallback and preserve threadTS/inboundTS.
func TestResolveMeta_CacheHitWins(t *testing.T) {
	c := &Channel{chatMetaM: make(map[string]*chatMeta), logger: zap.NewNop()}
	c.storeChatMeta("slack:C1:U2", &chatMeta{channelID: "C1", threadTS: "111.222", inboundTS: "333.444"})

	meta := c.resolveMeta(context.Background(), "slack:C1:U2")
	if meta == nil {
		t.Fatal("expected cache hit")
	}
	if meta.threadTS != "111.222" || meta.inboundTS != "333.444" {
		t.Errorf("cache hit lost fields: %+v", meta)
	}
}

func TestResolveMeta_InvalidChatID(t *testing.T) {
	c := &Channel{chatMetaM: make(map[string]*chatMeta), logger: zap.NewNop()}
	if meta := c.resolveMeta(context.Background(), "telegram:42"); meta != nil {
		t.Errorf("expected nil for non-slack chatID, got %+v", meta)
	}
}

// The headline durability path: cache cold, a persisted route exists → resolveMeta
// recovers full coordinates AND re-hydrates the cache for subsequent sends.
func TestResolveMeta_StoreHitRehydrates(t *testing.T) {
	c := &Channel{
		chatMetaM:  make(map[string]*chatMeta),
		logger:     zap.NewNop(),
		instanceID: "inst-1",
		store: &fakeRouteStore{route: &domain.SlackChatRoute{
			InstanceID:  "inst-1",
			ChatID:      "slack:C7:U8",
			ChannelID:   "C7",
			ThreadTS:    "999.111",
			SlackUserID: "U8",
			Locale:      "ru",
		}},
	}

	meta := c.resolveMeta(context.Background(), "slack:C7:U8")
	if meta == nil {
		t.Fatal("expected meta from persisted route")
	}
	if meta.channelID != "C7" || meta.threadTS != "999.111" || meta.userID != "U8" || meta.locale != "ru" {
		t.Errorf("route not mapped onto meta: %+v", meta)
	}
	if got := c.getChatMeta("slack:C7:U8"); got == nil || got.channelID != "C7" {
		t.Errorf("expected cache re-hydrated, got %+v", got)
	}
}

// A concurrent inbound entry must win over the persisted route (lost-update guard).
func TestResolveMeta_StoreHitDoesNotClobberCache(t *testing.T) {
	c := &Channel{
		chatMetaM:  make(map[string]*chatMeta),
		logger:     zap.NewNop(),
		instanceID: "inst-1",
		store: &fakeRouteStore{route: &domain.SlackChatRoute{
			InstanceID: "inst-1", ChatID: "slack:C7:U8", ChannelID: "C7", ThreadTS: "stale",
		}},
	}
	// Pre-seed a richer entry as if a concurrent inbound stored it.
	c.storeChatMeta("slack:C7:U8", &chatMeta{channelID: "C7", threadTS: "fresh", inboundTS: "abc"})

	// getChatMeta hits first here, but assert storeChatMetaIfAbsent also preserves
	// the richer entry even when reached via the store branch.
	if got := c.storeChatMetaIfAbsent("slack:C7:U8", &chatMeta{channelID: "C7", threadTS: "stale"}); got.threadTS != "fresh" || got.inboundTS != "abc" {
		t.Errorf("richer cache entry clobbered: %+v", got)
	}
}

// On a store error, resolveMeta must still fall through to the parse fallback.
func TestResolveMeta_StoreErrorFallsBackToParse(t *testing.T) {
	c := &Channel{
		chatMetaM:  make(map[string]*chatMeta),
		logger:     zap.NewNop(),
		instanceID: "inst-1",
		store:      &fakeRouteStore{err: errors.New("db down")},
	}
	meta := c.resolveMeta(context.Background(), "slack:D5")
	if meta == nil || meta.channelID != "D5" {
		t.Fatalf("expected parse fallback on store error, got %+v", meta)
	}
}

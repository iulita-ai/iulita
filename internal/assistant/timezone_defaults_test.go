package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/iulita-ai/iulita/internal/domain"
)

func TestTimezoneFallbackPreservesExplicitUTCProfile(t *testing.T) {
	a := newTestAssistant(t, &mockProvider{response: "ok"})
	ctx := context.Background()
	if got := a.resolveCurrentTime(ctx, "unknown-chat", ""); !strings.Contains(got, "UTC+8:00") {
		t.Fatalf("missing timezone did not use Irkutsk: %s", got)
	}
	user := &domain.User{ID: "admin-fixture", Username: "admin", PasswordHash: "fixture", Timezone: "UTC"}
	if err := a.store.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if got := a.resolveCurrentTime(ctx, "chat", user.ID); !strings.Contains(got, "UTC+0:00") {
		t.Fatalf("explicit UTC profile overridden: %s", got)
	}
	saved, err := a.store.GetUser(ctx, user.ID)
	if err != nil || saved.Timezone != "UTC" {
		t.Fatalf("profile changed: %v, %v", saved, err)
	}
	a.defaultTimezone = "Europe/Moscow"
	if got := a.resolveCurrentTime(ctx, "unknown-chat", ""); !strings.Contains(got, "UTC+3:00") {
		t.Fatalf("explicit application timezone overridden: %s", got)
	}
}

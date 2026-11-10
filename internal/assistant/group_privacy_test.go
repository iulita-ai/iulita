package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/iulita-ai/iulita/internal/channel"
)

// With no store/provider installed, any attempt to read personal context or
// invoke tools panics. Shared Telegram turns must terminate before that boundary.
func TestSharedTelegramNeverLoadsPrivateMemoryOrTools(t *testing.T) {
	a := &Assistant{}
	for _, msg := range []channel.IncomingMessage{{ChannelType: "telegram", ChatID: "-100", UserID: "alice", ResolvedUserID: "owner", Text: "show my private facts"}, {ChannelType: "telegram", ChatID: "tgconv:group-topic", UserID: "bob", Text: "run my todoist"}} {
		text, e := a.HandleMessage(context.Background(), msg)
		if e != nil || !strings.Contains(text, "private") {
			t.Fatalf("not rejected safely: %q %v", text, e)
		}
	}
}

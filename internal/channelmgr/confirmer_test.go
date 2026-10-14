package channelmgr

import (
	"testing"

	"github.com/iulita-ai/iulita/internal/skill/interact"
)

// TestManagerImplementsConfirmationAskerFactory is the regression test for
// the "no safe confirmation channel" incident: the assistant receives the
// channelmgr Manager as its prompter factory, so unless the Manager also
// implements ConfirmationAskerFactory, channels with working consent UIs
// (Telegram/Slack/Webchat/Console) are invisible to it and every mutating
// http_request call fails closed.
func TestManagerImplementsConfirmationAskerFactory(t *testing.T) {
	var _ interact.ConfirmationAskerFactory = (*Manager)(nil)
	var _ interact.PromptAskerFactory = (*Manager)(nil)
}

// TestConfirmerForUnknownChatIsNil: no channel owns the chat → no asker →
// the engine must fail closed rather than ask a stranger.
func TestConfirmerForUnknownChatIsNil(t *testing.T) {
	m := newTestManager(t)
	if c := m.ConfirmerFor("123456789"); c != nil {
		t.Fatalf("expected nil confirmer for unknown chat, got %T", c)
	}
	if c := m.ConfirmerFor("web:none"); c != nil {
		t.Fatalf("expected nil confirmer for unknown web chat, got %T", c)
	}
}

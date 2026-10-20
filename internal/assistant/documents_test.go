package assistant

import (
	"strings"
	"testing"

	"github.com/iulita-ai/iulita/internal/channel"
)

func TestDocumentMessageText(t *testing.T) {
	msg := channel.IncomingMessage{Documents: []channel.DocumentAttachment{{MimeType: "application/pdf"}}}
	if got := documentMessageText(msg); !strings.Contains(got, "save its summary to my facts") {
		t.Fatal(got)
	}
	msg.Text = "Only extract text; do not save anything"
	got := documentMessageText(msg)
	if !strings.HasPrefix(got, msg.Text) || strings.Contains(got, "save its summary to my facts") {
		t.Fatal(got)
	}
	msg.Documents = nil
	if got := documentMessageText(msg); got != msg.Text {
		t.Fatal(got)
	}
}

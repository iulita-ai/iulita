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

func TestTorrentStaysOpaque(t *testing.T) {
	doc := channel.DocumentAttachment{MimeType: "application/x-bittorrent", Filename: "test.torrent", Data: []byte("PRIVATE_BINARY_PAYLOAD")}
	if !isOpaqueDocument(doc) {
		t.Fatal("torrent would be sent to model")
	}
	msg := channel.IncomingMessage{Text: "Send to my Transmission paused", Documents: []channel.DocumentAttachment{doc}}
	got := documentMessageText(msg)
	if !strings.HasPrefix(got, msg.Text) || !strings.Contains(got, "http_attachment") || strings.Contains(got, string(doc.Data)) || strings.Contains(got, "save its summary") {
		t.Fatal(got)
	}
	doc.MimeType = "text/plain"
	if !isOpaqueDocument(doc) {
		t.Fatal("filename fallback would expose binary")
	}
}

package telegram

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/go-telegram/bot/models"
)

func TestRichNormalizationQuoteAndUTF16(t *testing.T) {
	var m models.Message
	err := json.Unmarshal([]byte(`{"message_id":7,"chat":{"id":1,"type":"private"},"rich_message":{"blocks":[{"type":"paragraph","text":["Explain ",{"type":"bold","text":"this"}]}]},"quote":{"text":"chosen","position":3},"reply_to_message":{"message_id":6,"chat":{"id":1,"type":"private"},"text":"😀 chosen text"}}`), &m)
	if err != nil {
		t.Fatal(err)
	}
	text, q := normalizedTelegramContent(&m)
	if text != "Explain this" || q == nil || q.Text != "chosen" || !q.Verified || q.SourceMessageID != 6 {
		t.Fatalf("text=%q quote=%+v", text, q)
	}
	m.ReplyToMessage.MessageThreadID = 99
	_, q = normalizedTelegramContent(&m)
	if q.Verified || q.SourceMessageID != 0 {
		t.Fatal("cross topic quote trusted")
	}
	if quoteAtUTF16("😀 chosen", "chosen", 2) {
		t.Fatal("wrong offset verified")
	}
}
func TestRichProjectionDropsHiddenDataAndBounds(t *testing.T) {
	var r models.RichMessage
	if err := json.Unmarshal([]byte(`{"blocks":[{"type":"paragraph","text":[{"type":"url","text":"label","url":"https://private.example"},{"type":"bank_card_number","text":"4111111111111111","bank_card_number":"4111111111111111"}]}]}`), &r); err != nil {
		t.Fatal(err)
	}
	text := richPlain(&r)
	if text != "label[redacted]" {
		t.Fatalf("unsafe projection %q", text)
	}
	large := richPlain(strings.Repeat("😀", 20000))
	if len(large) > richTextLimit || !utf8.ValidString(large) {
		t.Fatal("invalid bounded projection")
	}
}
func TestBookmarkWrongActorDoesNotConsume(t *testing.T) {
	rs := newRememberState()
	rs.store("remember:x", &rememberEntry{tgUserID: 1, tgChatID: -4, msgID: 3, createdAt: time.Now()})
	cq := &models.CallbackQuery{From: models.User{ID: 2}, Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: 3, Chat: models.Chat{ID: -4}}}}
	if _, ok := rs.takeBound("remember:x", cq); ok {
		t.Fatal("wrong actor accepted")
	}
	cq.From.ID = 1
	if _, ok := rs.takeBound("remember:x", cq); !ok {
		t.Fatal("owner lost button")
	}
	if _, ok := rs.takeBound("remember:x", cq); ok {
		t.Fatal("replay accepted")
	}
}
func TestManualPromptBindsActorAndTopic(t *testing.T) {
	ps := newPromptState()
	p := &pendingPrompt{replyCh: make(chan string, 1), otherMode: true, actor: 5, chat: -10, thread: 2, expires: time.Now().Add(time.Minute)}
	ps.pending["n"] = p
	if ps.HandleTextBound(-10, 2, 6, "wrong") || ps.HandleTextBound(-10, 3, 5, "wrong") {
		t.Fatal("foreign reply accepted")
	}
	if !ps.HandleTextBound(-10, 2, 5, "answer") {
		t.Fatal("owner rejected")
	}
	if got := <-p.replyCh; got != "answer" {
		t.Fatal(got)
	}
}

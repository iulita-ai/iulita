package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/iulita-ai/iulita/internal/channel"
)

func TestPrivateCallbackNoChatAndNoPublicRetry(t *testing.T) {
	calls := 0
	b, err := bot.New("123:fixture", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, &http.Client{Transport: presentationTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if err := req.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		if req.FormValue("chat_id") != "" {
			t.Fatal("callback delivery unexpectedly addresses a chat")
		}
		var params models.EphemeralMessageParameters
		if err := json.Unmarshal([]byte(req.FormValue("ephemeral_message_parameters")), &params); err != nil || params.CallbackQueryID != "query" || params.ReceiverUserID != 0 {
			t.Fatal("callback identity missing")
		}
		return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(`{"ok":false,"error_code":400,"description":"fixture error"}`)), Header: make(http.Header)}, nil
	})}))
	if err != nil {
		t.Fatal(err)
	}
	c := &Channel{bot: b, allowedIDs: map[int64]struct{}{7: {}}}
	if err := c.SendPrivateCallbackReply(context.Background(), &models.CallbackQuery{ID: "query", From: models.User{ID: 7}}, "private result"); err == nil {
		t.Fatal("failed transport reported success")
	}
	if calls != 1 {
		t.Fatalf("got %d sends", calls)
	}
	if err := c.SendPrivateCallbackReply(context.Background(), &models.CallbackQuery{ID: "query", From: models.User{ID: 8}}, "private result"); err == nil {
		t.Fatal("unauthorized actor accepted")
	}
	if calls != 1 {
		t.Fatal("unauthorized send attempted")
	}
}

func TestPromptCallbackBindsMessageActorAndOneShot(t *testing.T) {
	b, err := bot.New("123:fixture", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, &http.Client{Transport: presentationTransport(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":true}`)), Header: make(http.Header)}, nil
	})}))
	if err != nil {
		t.Fatal(err)
	}
	ps := newPromptState()
	p := &pendingPrompt{replyCh: make(chan string, 1), options: map[string]string{"prompt:nonce:0": "yes"}, actor: 5, chat: -10, thread: 2, message: 4, expires: time.Now().Add(time.Minute)}
	ps.pending["nonce"] = p
	cq := &models.CallbackQuery{ID: "q", Data: "prompt:nonce:0", From: models.User{ID: 6}, Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: 4, MessageThreadID: 2, Chat: models.Chat{ID: -10}}}}
	ps.HandleCallback(b, cq)
	select {
	case <-p.replyCh:
		t.Fatal("wrong actor accepted")
	default:
	}
	cq.From.ID = 5
	cq.Message.Message.ID = 3
	ps.HandleCallback(b, cq)
	select {
	case <-p.replyCh:
		t.Fatal("wrong message accepted")
	default:
	}
	cq.Message.Message.ID = 4
	ps.HandleCallback(b, cq)
	select {
	case got := <-p.replyCh:
		if got != "yes" {
			t.Fatal(got)
		}
	default:
		t.Fatal("owner rejected")
	}
	ps.HandleCallback(b, cq)
	select {
	case <-p.replyCh:
		t.Fatal("replay accepted")
	default:
	}
}

func TestOutgoingRichQuoteUsesVerifiedSourceAndPreservesSelection(t *testing.T) {
	calls := 0
	b, err := bot.New("123:fixture", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, &http.Client{Transport: presentationTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if err := req.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(req.URL.Path, "/sendRichMessage") {
			t.Fatal("wrong method")
		}
		var reply models.ReplyParameters
		if err := json.Unmarshal([]byte(req.FormValue("reply_parameters")), &reply); err != nil {
			t.Fatal(err)
		}
		if reply.MessageID != 4 || reply.QuotePosition != 3 || len(reply.QuoteRichTexts) != 1 || reply.QuoteRichTexts[0].PlainText != "chosen" {
			t.Fatalf("quote lost: %+v", reply)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":8,"chat":{"id":7,"type":"private"}}}`)), Header: make(http.Header)}, nil
	})}))
	if err != nil {
		t.Fatal(err)
	}
	c := &Channel{bot: b}
	target := channel.TelegramTarget{ChatID: 7, ThreadID: 2}
	q := &channel.QuoteContext{Text: "chosen", SourceMessageID: 4, SourceChatID: 7, SourceThreadID: 2, Position: 3, SourceRich: true}
	if c.sendRichQuoteReply(context.Background(), target, "answer", q, "7", "owner", "en", false) {
		t.Fatal("unverified source accepted")
	}
	q.Verified = true
	if !c.sendRichQuoteReply(context.Background(), target, "answer", q, "7", "owner", "en", false) {
		t.Fatal("verified source rejected")
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}

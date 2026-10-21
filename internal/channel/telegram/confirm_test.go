package telegram

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/iulita-ai/iulita/internal/skill/interact"
	"go.uber.org/zap"
)

func TestConfirmationNonceBindsActorChatMessageExpiryAndVerdict(t *testing.T) {
	for _, invalid := range []struct {
		name, verdict string
		sender, chat  int64
		message       int
		expired       bool
	}{
		{"other actor", "1", 8, 7, 9, false}, {"other chat", "1", 7, 8, 9, false}, {"other message", "1", 7, 7, 8, false}, {"standing SSH", "G", 7, 7, 9, false}, {"unknown verdict", "wat", 7, 7, 9, false}, {"expired", "1", 7, 7, 9, true},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			cs := newConfirmState()
			expires := time.Now().Add(time.Minute)
			if invalid.expired {
				expires = time.Now().Add(-time.Minute)
			}
			cs.set("nonce", &pendingConfirm{expectFrom: 7, chatID: 7, msgID: 9, expires: expires, reason: interact.ConfirmSSHCommand, replyCh: make(chan string, 1)})
			if _, ok := cs.takeBound("nonce", invalid.verdict, invalid.sender, invalid.chat, invalid.message); ok {
				t.Fatal("invalid callback accepted")
			}
			if invalid.expired {
				return
			}
			if _, ok := cs.takeBound("nonce", "1", 7, 7, 9); !ok {
				t.Fatal("invalid callback consumed valid owner's nonce")
			}
			if _, ok := cs.takeBound("nonce", "1", 7, 7, 9); ok {
				t.Fatal("replay accepted")
			}
		})
	}
}

func TestConfirmationDoesNotSupersedeAnotherUsersChat(t *testing.T) {
	cs := newConfirmState()
	old := &pendingConfirm{expectFrom: 1, chatID: 1, replyCh: make(chan string, 1)}
	cs.set("old", old)
	cs.set("new", &pendingConfirm{expectFrom: 2, chatID: 2, replyCh: make(chan string, 1)})
	select {
	case <-old.replyCh:
		t.Fatal("unrelated prompt rejected")
	default:
	}
}

func TestTelegramSSHApprovalResolvesKeyboardAndDeliversOnlyPrivateDocument(t *testing.T) {
	var keyboard, edited, document bool
	b, err := bot.New("123:fixture", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, &http.Client{Transport: presentationTransport(func(req *http.Request) (*http.Response, error) {
		if err := req.ParseMultipartForm(2 << 20); err != nil {
			t.Error(err)
		}
		switch {
		case strings.HasSuffix(req.URL.Path, "/sendMessage"):
			keyboard = true
			if strings.Contains(req.FormValue("reply_markup"), "Always") {
				t.Error("SSH offered standing approval")
			}
		case strings.HasSuffix(req.URL.Path, "/editMessageText"):
			edited = true
			if !strings.Contains(req.FormValue("text"), "Approved") || strings.Contains(req.FormValue("reply_markup"), "callback_data") {
				t.Error("resolved approval retained active keyboard")
			}
		case strings.HasSuffix(req.URL.Path, "/sendDocument"):
			document = true
			if req.FormValue("chat_id") != "7" || req.FormValue("protect_content") != "true" || req.FormValue("parse_mode") != "" {
				t.Error("private document isolation missing")
			}
			f, _, e := req.FormFile("document")
			if e != nil {
				t.Error(e)
			} else {
				defer f.Close()
				data, _ := io.ReadAll(f)
				if !strings.Contains(string(data), "raw <b>output</b>") {
					t.Error("output altered")
				}
			}
		case strings.HasSuffix(req.URL.Path, "/answerCallbackQuery"):
		default:
			t.Errorf("unexpected method %s", req.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":9}}`))}, nil
	})}))
	if err != nil {
		t.Fatal(err)
	}
	c := &Channel{bot: b, logger: zap.NewNop(), confirms: newConfirmState()}
	tc := &telegramConfirmer{channel: c, chatID: 7, fromTg: 7}
	done := make(chan error, 1)
	go func() {
		_, e := tc.Confirm(context.Background(), interact.Confirmation{Nonce: "nonce", OperationID: "op", Reason: interact.ConfirmSSHCommand, ExpiresAt: time.Now().Add(time.Second)}, interact.Actor{UserID: "owner"})
		done <- e
	}()
	deadline := time.Now().Add(time.Second)
	for {
		c.confirms.mu.Lock()
		ready := c.confirms.pending["nonce"] != nil
		c.confirms.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no pending prompt")
		}
		time.Sleep(time.Millisecond)
	}
	c.HandleConfirmCallback(&models.CallbackQuery{Data: "cfm:nonce:1", From: models.User{ID: 7}, Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: 9, Chat: models.Chat{ID: 7}}}})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !keyboard || !edited {
		t.Fatal("approval message did not resolve")
	}
	if err := tc.DeliverPrivateResult(context.Background(), interact.Actor{UserID: "other"}, "op", "raw <b>output</b>"); err == nil {
		t.Fatal("other owner received output")
	}
	if err := tc.DeliverPrivateResult(context.Background(), interact.Actor{UserID: "owner"}, "op", "raw <b>output</b>"); err != nil {
		t.Fatal(err)
	}
	if !document {
		t.Fatal("no result document")
	}
	if err := tc.DeliverPrivateResult(context.Background(), interact.Actor{UserID: "owner"}, "op", "replay"); err == nil {
		t.Fatal("delivery replay accepted")
	}
	tc.chatID = -7
	tc.approved["op"] = "owner"
	if err := tc.DeliverPrivateResult(context.Background(), interact.Actor{UserID: "owner"}, "op", "group leak"); err == nil {
		t.Fatal("group output allowed")
	}
}

func TestConfirmationExpiryClearsKeyboardWhenTextEditFails(t *testing.T) {
	cleared := false
	b, err := bot.New("123:fixture", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, &http.Client{Transport: presentationTransport(func(req *http.Request) (*http.Response, error) {
		if err := req.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
		}
		body := `{"ok":true,"result":{"message_id":9}}`
		if strings.HasSuffix(req.URL.Path, "/editMessageText") {
			body = `{"ok":false,"error_code":400,"description":"text edit unavailable"}`
		}
		if strings.HasSuffix(req.URL.Path, "/editMessageReplyMarkup") {
			cleared = true
			if strings.Contains(req.FormValue("reply_markup"), "callback_data") {
				t.Error("fallback retained keyboard")
			}
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}))
	if err != nil {
		t.Fatal(err)
	}
	c := &Channel{bot: b, logger: zap.NewNop(), confirms: newConfirmState()}
	tc := &telegramConfirmer{channel: c, chatID: 7, fromTg: 7}
	_, err = tc.Confirm(context.Background(), interact.Confirmation{Nonce: "expired", OperationID: "expired", Reason: interact.ConfirmSSHCommand, ExpiresAt: time.Now().Add(10 * time.Millisecond)}, interact.Actor{UserID: "owner"})
	if !errors.Is(err, interact.ErrPromptTimeout) || !cleared {
		t.Fatal("expiry did not retire keyboard after text-edit rejection")
	}
	c.confirms.mu.Lock()
	defer c.confirms.mu.Unlock()
	if len(c.confirms.pending) != 0 {
		t.Fatal("expired nonce retained")
	}
}

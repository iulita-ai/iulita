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
)

func TestTextPromptReservesLaneBeforeSendAndCleansUp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started, release := make(chan struct{}), make(chan struct{})
	b, err := bot.New("123:fixture", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, &http.Client{Transport: presentationTransport(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":11,"chat":{"id":42,"type":"private"}}}`))}, nil
	})}))
	if err != nil {
		t.Fatal(err)
	}
	c := &Channel{bot: b, prompts: newPromptState()}
	p := &telegramPrompter{channel: c, prompts: c.prompts, chatID: 42, actor: 42, threadID: 7}
	done := make(chan error, 1)
	go func() { _, e := p.Ask(ctx, "Name?", nil); done <- e }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("prompt did not send")
	}
	if _, e := p.Ask(ctx, "Duplicate", nil); !errors.Is(e, errPromptPending) {
		t.Fatalf("duplicate=%v", e)
	}
	if c.prompts.HandleTextBound(42, 7, 43, "Wrong") {
		t.Fatal("wrong actor consumed reservation")
	}
	if !c.prompts.HandleTextBound(42, 7, 42, "/cancel") {
		t.Fatal("cancel unavailable while sending")
	}
	close(release)
	select {
	case e := <-done:
		if !errors.Is(e, interact.ErrCancelled) {
			t.Fatal(e)
		}
	case <-ctx.Done():
		t.Fatal("prompt did not cancel")
	}
	c.prompts.mu.Lock()
	remaining := len(c.prompts.pending)
	c.prompts.mu.Unlock()
	if remaining != 0 {
		t.Fatal("prompt reservation leaked")
	}
}

func TestTextPromptCancelButtonAndContext(t *testing.T) {
	for _, button := range []bool{true, false} {
		ctx, cancel := context.WithCancel(context.Background())
		ps := newPromptState()
		var p *telegramPrompter
		b, e := bot.New("123:fixture", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, &http.Client{Transport: presentationTransport(func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "/sendMessage") {
				go func() {
					if !button {
						cancel()
						return
					}
					deadline := time.Now().Add(time.Second)
					for time.Now().Before(deadline) {
						ps.mu.Lock()
						data := ""
						for _, pending := range ps.pending {
							if pending.message == 11 {
								for k, v := range pending.options {
									if v == "__cancel__" {
										data = k
									}
								}
							}
						}
						ps.mu.Unlock()
						if data != "" {
							ps.HandleCallback(p.channel.bot, &models.CallbackQuery{ID: "cancel", Data: data, From: models.User{ID: 42}, Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: 11, Chat: models.Chat{ID: 42}, MessageThreadID: 7}}})
							return
						}
						time.Sleep(time.Millisecond)
					}
					cancel()
				}()
			}
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":11,"chat":{"id":42,"type":"private"}}}`))}, nil
		})}))
		if e != nil {
			cancel()
			t.Fatal(e)
		}
		p = &telegramPrompter{channel: &Channel{bot: b}, prompts: ps, chatID: 42, actor: 42, threadID: 7}
		_, e = p.Ask(ctx, "Name?", nil)
		cancel()
		if !errors.Is(e, interact.ErrCancelled) {
			t.Fatal(e)
		}
		ps.mu.Lock()
		n := len(ps.pending)
		ps.mu.Unlock()
		if n != 0 {
			t.Fatal("cancel left pending prompt")
		}
	}
}

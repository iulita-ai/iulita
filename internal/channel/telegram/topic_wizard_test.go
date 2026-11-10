package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
	"go.uber.org/zap"
)

func TestExplicitTopicWizard(t *testing.T) {
	for _, tc := range []struct {
		name    string
		thread  int
		cancel  bool
		approve bool
		invalid bool
	}{
		{name: "create from root", approve: true},
		{name: "create from topic", thread: 55, approve: true},
		{name: "cancel name", cancel: true},
		{name: "reject confirmation"},
		{name: "reject sensitive name before confirmation", invalid: true, approve: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			s, err := sqlite.New(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err = s.RunMigrations(ctx); err != nil {
				t.Fatal(err)
			}
			type event struct {
				text    string
				buttons models.InlineKeyboardMarkup
			}
			events := make(chan event, 8)
			var creates atomic.Int32
			b, err := bot.New("123:fixture", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, &http.Client{Transport: presentationTransport(func(r *http.Request) (*http.Response, error) {
				_ = r.ParseMultipartForm(1 << 20)
				result := `true`
				switch {
				case strings.HasSuffix(r.URL.Path, "/sendMessage"):
					var kb models.InlineKeyboardMarkup
					_ = json.Unmarshal([]byte(r.FormValue("reply_markup")), &kb)
					events <- event{r.FormValue("text"), kb}
					result = `{"message_id":11,"chat":{"id":42,"type":"private"}}`
				case strings.HasSuffix(r.URL.Path, "/createForumTopic"):
					creates.Add(1)
					if r.FormValue("name") != "Исследования" {
						t.Errorf("wrong name %q", r.FormValue("name"))
					}
					result = `{"message_thread_id":77,"name":"Исследования","icon_color":0}`
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":` + result + `}`))}, nil
			})}))
			if err != nil {
				t.Fatal(err)
			}
			c := &Channel{bot: b, instanceID: "a", username: "BotAI", store: s, logger: zap.NewNop(), sentMsgs: newSentTracker(), confirms: newConfirmState(), prompts: newPromptState()}
			m := &models.Message{ID: 10, Text: "/topic@BotAI new", Chat: models.Chat{ID: 42, Type: models.ChatTypePrivate}, From: &models.User{ID: 42}, MessageThreadID: tc.thread}
			key, err := c.conversationFor(ctx, m)
			if err != nil {
				t.Fatal(err)
			}
			if err = c.persistRoute(ctx, key, m, "alice"); err != nil {
				t.Fatal(err)
			}
			if !c.handleTopicLifecycle(ctx, m, key, "alice") {
				t.Fatal("explicit create command not handled")
			}
			next := func() event {
				t.Helper()
				select {
				case e := <-events:
					return e
				case <-ctx.Done():
					t.Fatal("timed out waiting for bot")
					return event{}
				}
			}
			prompt := next()
			if !strings.Contains(prompt.text, "Как назвать") || len(prompt.buttons.InlineKeyboard) != 1 {
				t.Fatalf("not immediate text + cancel: %+v", prompt)
			}
			if creates.Load() != 0 {
				t.Fatal("created before name")
			}
			if c.prompts.HandleTextBound(42, tc.thread, 43, "Foreign") || c.prompts.HandleTextBound(42, tc.thread+1, 42, "Foreign") || c.prompts.HandleTextBound(42, tc.thread, 42, "/topic help") {
				t.Fatal("foreign answer or command consumed as name")
			}
			if tc.cancel {
				if !c.prompts.HandleTextBound(42, tc.thread, 42, "/cancel") {
					t.Fatal("cancel not handled")
				}
			} else {
				if tc.invalid {
					if !c.prompts.HandleTextBound(42, tc.thread, 42, "4111 1111 1111 1111") {
						t.Fatal("invalid answer not handled")
					}
					retry := next()
					if !strings.Contains(retry.text, "Sensitive topic name rejected") || strings.Contains(retry.text, "4111") || creates.Load() != 0 {
						t.Fatal("invalid name leaked or reached confirmation")
					}
				}
				if !c.prompts.HandleTextBound(42, tc.thread, 42, "  Исследования  ") {
					t.Fatal("name not handled")
				}
				confirmation := next()
				if !strings.Contains(confirmation.text, "Исследования") || creates.Load() != 0 {
					t.Fatal("missing preview or early mutation")
				}
				index := 0
				if !tc.approve {
					index = 1
				}
				data := confirmation.buttons.InlineKeyboard[0][index].CallbackData
				// Confirm registers after SendMessage returns.
				for {
					c.confirms.mu.Lock()
					ready := len(c.confirms.pending) > 0
					c.confirms.mu.Unlock()
					if ready {
						break
					}
					select {
					case <-ctx.Done():
						t.Fatal("confirmation not registered")
					case <-time.After(time.Millisecond):
					}
				}
				c.HandleConfirmCallback(&models.CallbackQuery{ID: "decision", From: models.User{ID: 42}, Data: data, Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: 11, Chat: models.Chat{ID: 42}, MessageThreadID: tc.thread}}})
			}
			done := make(chan struct{})
			go func() { c.wg.Wait(); close(done) }()
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("wizard did not stop")
			}
			want := int32(0)
			if tc.approve {
				want = 1
			}
			if creates.Load() != want {
				t.Fatalf("creates=%d want=%d", creates.Load(), want)
			}
			if tc.approve {
				target, e := c.resolveTarget(ctx, c.routeKey(42, 77))
				if e != nil || target.OwnerID != "alice" || target.ThreadID != 77 {
					t.Fatalf("route=%+v err=%v", target, e)
				}
			}
		})
	}
}

func TestTopicNameValidation(t *testing.T) {
	for _, name := range []string{"", "  ", "/topic", "name\nother", strings.Repeat("я", 129), "4111 1111 1111 1111"} {
		if validateTopicName(name) == nil {
			t.Errorf("accepted invalid name %q", name)
		}
	}
}

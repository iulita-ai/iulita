package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
	"go.uber.org/zap"
)

func TestTopicLifecycleApprovedPrivateFlow(t *testing.T) {
	store, e := sqlite.New(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if e = store.RunMigrations(ctx); e != nil {
		t.Fatal(e)
	}
	var c *Channel
	var creates atomic.Int32
	transport := presentationTransport(func(r *http.Request) (*http.Response, error) {
		_ = r.ParseMultipartForm(1 << 20)
		result := `{"message_id":11,"chat":{"id":42,"type":"private"}}`
		switch {
		case strings.HasSuffix(r.URL.Path, "/createForumTopic"):
			creates.Add(1)
			result = `{"message_thread_id":77,"name":"Research","icon_color":0}`
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			if strings.Contains(r.FormValue("text"), "Confirmation required") {
				var kb models.InlineKeyboardMarkup
				if parseErr := json.Unmarshal([]byte(r.FormValue("reply_markup")), &kb); parseErr != nil {
					t.Fatal(parseErr)
				}
				data := kb.InlineKeyboard[0][0].CallbackData
				go func() {
					deadline := time.Now().Add(time.Second)
					for time.Now().Before(deadline) {
						c.confirms.mu.Lock()
						n := len(c.confirms.pending)
						c.confirms.mu.Unlock()
						if n > 0 {
							c.HandleConfirmCallback(&models.CallbackQuery{ID: "approve", From: models.User{ID: 42}, Data: data, Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: 11, Chat: models.Chat{ID: 42}}}})
							return
						}
						runtime.Gosched()
					}
				}()
			}
		case strings.HasSuffix(r.URL.Path, "/closeForumTopic"), strings.HasSuffix(r.URL.Path, "/reopenForumTopic"):
			t.Error("private topic pause/resume invoked unsupported native API")
			result = `false`
		case strings.HasSuffix(r.URL.Path, "/editForumTopic"), strings.HasSuffix(r.URL.Path, "/deleteForumTopic"), strings.HasSuffix(r.URL.Path, "/answerCallbackQuery"):
			result = `true`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":` + result + `}`)), Header: http.Header{}}, nil
	})
	b, e := bot.New("123:fixture", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, &http.Client{Transport: transport}))
	if e != nil {
		t.Fatal(e)
	}
	c = &Channel{bot: b, botID: 123, instanceID: "a", username: "BotAI", store: store, logger: zap.NewNop(), sentMsgs: newSentTracker(), confirms: newConfirmState()}
	root := &models.Message{ID: 10, Chat: models.Chat{ID: 42, Type: models.ChatTypePrivate}, From: &models.User{ID: 42}}
	if e = c.persistRoute(ctx, "42", root, "alice"); e != nil {
		t.Fatal(e)
	}
	c.runTopicLifecycle(ctx, root, "42", "alice", "new", "Research")
	if creates.Load() != 1 {
		t.Fatalf("create count %d", creates.Load())
	}
	topic := *root
	topic.MessageThreadID = 77
	key, _ := c.conversationFor(ctx, &topic)
	if target, e := c.resolveTarget(ctx, key); e != nil || target.ThreadID != 77 {
		t.Fatalf("created route missing: %+v %v", target, e)
	}
	for i, action := range []string{"rename", "close", "reopen", "delete"} {
		topic.ID = 20 + i
		source := topic
		if action == "reopen" {
			source.MessageThreadID = 0
		}
		c.runTopicLifecycle(ctx, &source, key, "alice", action, "Renamed")
		state, e := store.GetTelegramTopicState(ctx, key)
		if e != nil || state == nil {
			t.Fatal(state, e)
		}
		if action == "close" && state.Status != "paused" {
			t.Fatalf("close alias did not pause locally: %+v", state)
		}
		if action == "reopen" && state.Status != "open" {
			t.Fatalf("reopen alias did not resume locally: %+v", state)
		}
		if action == "close" || action == "delete" {
			if _, e = c.resolveTarget(ctx, key); e == nil {
				t.Fatal("closed/deleted topic accepted delivery")
			}
		}
	}
	c.runTopicLifecycle(ctx, root, "42", "alice", "new", "Research")
	if creates.Load() != 1 {
		t.Fatal("replayed create repeated external mutation")
	}
}

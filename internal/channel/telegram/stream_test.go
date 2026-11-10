package telegram

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
	"go.uber.org/zap"
)

func TestBufferedStreamNeverPublishesPartialsAndClassifiesFinal(t *testing.T) {
	store, e := sqlite.New(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	ctx := context.Background()
	if e = store.RunMigrations(ctx); e != nil {
		t.Fatal(e)
	}
	calls := 0
	b, e := bot.New("123:fixture", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, &http.Client{Transport: presentationTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		_ = r.ParseMultipartForm(1 << 20)
		if r.FormValue("ephemeral_message_parameters") == "" {
			t.Fatal("sensitive final became public")
		}
		if r.FormValue("message_thread_id") != "77" {
			t.Fatal("lost stream thread")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":1}}`)), Header: http.Header{}}, nil
	})}))
	if e != nil {
		t.Fatal(e)
	}
	c := &Channel{bot: b, store: store, instanceID: "a", logger: zap.NewNop(), sentMsgs: newSentTracker()}
	m := &models.Message{Chat: models.Chat{ID: 42, Type: models.ChatTypePrivate}, From: &models.User{ID: 42}, MessageThreadID: 77}
	key, _ := c.conversationFor(ctx, m)
	if e = c.persistRoute(ctx, key, m, "alice"); e != nil {
		t.Fatal(e)
	}
	edit, done, e := c.StartStream(ctx, key, 9)
	if e != nil {
		t.Fatal(e)
	}
	edit("password")
	edit("password=abcdefgh")
	if calls != 0 {
		t.Fatal("unclassified partial sent")
	}
	done("password=abcdefgh")
	done("password=abcdefgh")
	edit("late")
	if calls != 1 {
		t.Fatalf("duplicate final %d", calls)
	}
	if _, ok := c.sentMsgs.text(42, 1); ok {
		t.Fatal("sensitive final cached")
	}
	_, pausedFinal, e := c.StartStream(ctx, key, 9)
	if e != nil {
		t.Fatal(e)
	}
	if e = store.PutTelegramTopicState(ctx, &domain.TelegramTopicState{ConversationID: key, Status: "paused"}); e != nil {
		t.Fatal(e)
	}
	pausedFinal("password=abcdefgh")
	if calls != 1 {
		t.Fatal("in-flight stream published after pause")
	}
	if e = store.PutTelegramTopicState(ctx, &domain.TelegramTopicState{ConversationID: key, Status: "open"}); e != nil {
		t.Fatal(e)
	}
	managed, _ := channel.WithResponsePresentation(ctx)
	_, final, e := c.StartStream(managed, key, 9)
	if e != nil {
		t.Fatal(e)
	}
	final("normal")
	if calls != 1 {
		t.Fatal("channel-owned turn double publication")
	}
	cancelled, cancel := context.WithCancel(ctx)
	_, final, e = c.StartStream(cancelled, key, 9)
	if e != nil {
		t.Fatal(e)
	}
	cancel()
	final("password=abcdefgh")
	if calls != 1 {
		t.Fatal("cancelled output published")
	}
}

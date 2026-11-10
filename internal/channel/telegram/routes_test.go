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
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
	"go.uber.org/zap"
)

func TestTopicIdentityAndOwnerLink(t *testing.T) {
	s, e := sqlite.New(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	ctx := context.Background()
	if e = s.RunMigrations(ctx); e != nil {
		t.Fatal(e)
	}
	a := &Channel{instanceID: "a", username: "IulitaAI", store: s}
	b := &Channel{instanceID: "b", store: s}
	m := &models.Message{Chat: models.Chat{ID: 42, Type: models.ChatTypePrivate}, From: &models.User{ID: 42}, MessageThreadID: 77}
	id, e := a.conversationFor(ctx, m)
	if e != nil {
		t.Fatal(e)
	}
	if id == a.routeKey(42, 78) || id == b.routeKey(42, 77) || id == "42" {
		t.Fatal("conversation collision")
	}
	if e = a.persistRoute(ctx, id, m, "alice"); e != nil {
		t.Fatal(e)
	}
	target, e := a.resolveTarget(ctx, id)
	if e != nil || target.ChatID != 42 || target.ThreadID != 77 || !target.Private {
		t.Fatalf("bad target %+v %v", target, e)
	}
	if _, e = b.resolveTarget(ctx, id); e == nil {
		t.Fatal("wrong bot accepted route")
	}
	if _, e = a.TopicLink(ctx, id, "bob"); e == nil {
		t.Fatal("wrong owner accepted")
	}
	link, e := a.TopicLink(ctx, id, "alice")
	if e != nil || link != "https://t.me/IulitaAI/77" {
		t.Fatalf("bad link %q %v", link, e)
	}
	if _, e = a.resolveTarget(ctx, "tgconv:missing"); e == nil {
		t.Fatal("unknown route accepted")
	}
}

func TestTopicProactiveDeliveryCarriesThreadAndFailure(t *testing.T) {
	s, e := sqlite.New(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	ctx := context.Background()
	if e = s.RunMigrations(ctx); e != nil {
		t.Fatal(e)
	}
	calls := 0
	b, e := bot.New("123:fixture", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, &http.Client{Transport: presentationTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if parseErr := req.ParseMultipartForm(1 << 20); parseErr != nil {
			t.Fatal(parseErr)
		}
		if req.FormValue("chat_id") != "42" || req.FormValue("message_thread_id") != "77" {
			t.Fatalf("wrong delivery coordinates: %+v", req.Form)
		}
		return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(`{"ok":false,"error_code":400,"description":"test refusal"}`)), Header: http.Header{}}, nil
	})}))
	if e != nil {
		t.Fatal(e)
	}
	c := &Channel{bot: b, instanceID: "a", username: "BotAI", store: s, logger: zap.NewNop(), sentMsgs: newSentTracker()}
	m := &models.Message{Chat: models.Chat{ID: 42, Type: models.ChatTypePrivate}, From: &models.User{ID: 42}, MessageThreadID: 77}
	id, _ := c.conversationFor(ctx, m)
	if e = c.persistRoute(ctx, id, m, "alice"); e != nil {
		t.Fatal(e)
	}
	if e = c.SendMessage(ctx, id, "test"); e == nil {
		t.Fatal("delivery failure swallowed")
	}
	if calls == 0 {
		t.Fatal("delivery not attempted")
	}
	before := calls
	if e = c.SendMessage(ctx, "tgconv:unknown", "test"); e == nil || calls != before {
		t.Fatal("missing route fell back to another destination")
	}
}

func TestDayCommandIgnoresAttachmentOnlyAndForeignAddress(t *testing.T) {
	c := &Channel{username: "OriginalBot", day: &DayController{}}
	for _, text := range []string{"", "   ", "/day@AnotherBot"} {
		if c.handleDayCommand(context.Background(), &models.Message{Text: text}) {
			t.Fatalf("unexpected command interception %q", text)
		}
	}
}

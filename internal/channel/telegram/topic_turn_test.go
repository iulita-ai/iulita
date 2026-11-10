package telegram

import (
	"context"
	"fmt"
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

func TestRootTurnStartsTopicExactlyOnce(t *testing.T) {
	for _, scenario := range []string{"", "create", "copy", "second_instance"} {
		failure := scenario
		collision := scenario == "second_instance"
		if collision {
			failure = ""
		}
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			s, e := sqlite.New(":memory:")
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			if e = s.RunMigrations(ctx); e != nil {
				t.Fatal(e)
			}
			creates, copies := 0, 0
			var output []string
			b, e := bot.New("123:fixture", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, &http.Client{Transport: presentationTransport(func(r *http.Request) (*http.Response, error) {
				_ = r.ParseMultipartForm(1 << 20)
				result := `true`
				fail := false
				switch {
				case strings.HasSuffix(r.URL.Path, "/createForumTopic"):
					creates++
					fail = failure == "create"
					result = `{"message_thread_id":77,"name":"Новый проект"}`
				case strings.HasSuffix(r.URL.Path, "/copyMessage"):
					copies++
					fail = failure == "copy"
					result = fmt.Sprintf(`{"message_id":%d}`, 300+copies)
					if r.FormValue("chat_id") != "42" || r.FormValue("from_chat_id") != "42" || r.FormValue("message_thread_id") != "77" {
						t.Error("seed copied across scope")
					}
				case strings.HasSuffix(r.URL.Path, "/sendMessage"):
					output = append(output, r.FormValue("text"))
					result = `{"message_id":12,"chat":{"id":42,"type":"private"}}`
				}
				if fail {
					return &http.Response{StatusCode: 500, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"ok":false,"error_code":500,"description":"synthetic lost result"}`))}, nil
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":` + result + `}`))}, nil
			})}))
			if e != nil {
				t.Fatal(e)
			}
			c := &Channel{bot: b, botID: 123, topicsEnabled: true, instanceID: "test", username: "BotAI", allowedIDs: map[int64]struct{}{42: {}}, store: s, sentMsgs: newSentTracker(), confirmSenders: map[string]int64{}, logger: zap.NewNop()}
			m := &models.Message{ID: 10, Text: "Новый проект", Chat: models.Chat{ID: 42, Type: models.ChatTypePrivate}, From: &models.User{ID: 42}, MediaGroupID: "album"}
			if collision {
				other := &Channel{instanceID: "other", store: s}
				if e = other.persistRoute(ctx, "42", m, "alice"); e != nil {
					t.Fatal(e)
				}
			}
			rootKey, e := c.conversationFor(ctx, m)
			if e != nil {
				t.Fatal(e)
			}
			if collision && !strings.HasPrefix(rootKey, "tgconv:") {
				t.Fatal("missing second-instance root scope")
			}
			if e = c.persistRoute(ctx, rootKey, m, "alice"); e != nil {
				t.Fatal(e)
			}
			original := channel.IncomingMessage{ChatID: rootKey, MessageID: 10, UserID: "42", ResolvedUserID: "alice", ProvenPrivateRoute: true, Text: m.Text, ChannelInstanceID: "test", ChannelType: "telegram"}
			msg := original
			ok := c.prepareTopicTurn(ctx, m, &msg)
			if ok != (failure == "") {
				t.Fatalf("accepted=%v failure=%s", ok, failure)
			}
			if failure == "" {
				if msg.ChatID == "42" || msg.MessageID != 301 || msg.ResolvedUserID != "alice" || !msg.ProvenPrivateRoute {
					t.Fatalf("bad seed turn %+v", msg)
				}
				r, routeErr := c.resolveTarget(ctx, msg.ChatID)
				if routeErr != nil || r.ThreadID != 77 || r.OwnerID != "alice" {
					t.Fatal(r, routeErr)
				}
				if e = s.FinishTelegramTopicSeed(ctx, "test", "alice", 42, 77, 301, "processed"); e != nil {
					t.Fatal(e)
				}
			}
			replay := original
			beforeCopies := copies
			if c.prepareTopicTurn(ctx, m, &replay) || creates != 1 || copies != beforeCopies {
				t.Fatal("replayed uncertain/create/copy/processed seed")
			}
			if failure == "" {
				// A second item in the same album shares the original topic.
				m.ID = 11
				next := original
				next.MessageID = 11
				if !c.prepareTopicTurn(ctx, m, &next) || creates != 1 || copies != 2 || next.ChatID != msg.ChatID {
					t.Fatal("album split into separate topics")
				}
				if action, name, ok := parseTopicCommand("/topic"); !ok || action != "list" || name != "" {
					t.Fatal("bare command still creates")
				}
				if !c.handleTopicLifecycle(ctx, &models.Message{ID: 20, Text: "/topic", Chat: m.Chat, From: m.From}, "42", "alice") {
					t.Fatal("list not handled")
				}
				c.wg.Wait()
				if creates != 1 || !strings.Contains(output[len(output)-1], "https://t.me/BotAI/77") {
					t.Fatal("list created or omitted topic")
				}
				if e = s.PutTelegramTopicState(ctx, &domain.TelegramTopicState{ConversationID: msg.ChatID, Name: "Новый проект", Status: "paused"}); e != nil {
					t.Fatal(e)
				}
				c.listTopics(ctx, m, "alice", "")
				if !strings.Contains(output[len(output)-1], "paused") {
					t.Fatal("paused topic hidden")
				}
			}
		})
	}
}

func TestRootCommandsAndExistingTopicsDoNotCreate(t *testing.T) {
	c := &Channel{topicsEnabled: true}
	for _, m := range []*models.Message{
		{Text: "/topic", Chat: models.Chat{ID: 42, Type: models.ChatTypePrivate}, From: &models.User{ID: 42}},
		{Text: "hello", MessageThreadID: 77, Chat: models.Chat{ID: 42, Type: models.ChatTypePrivate}, From: &models.User{ID: 42}},
		{Text: "hello", Chat: models.Chat{ID: -42, Type: models.ChatTypeGroup}, From: &models.User{ID: 42}},
	} {
		msg := channel.IncomingMessage{Text: m.Text}
		if !c.prepareTopicTurn(context.Background(), m, &msg) {
			t.Fatal("non-root turn intercepted")
		}
	}
}

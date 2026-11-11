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
	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
	"go.uber.org/zap"
)

func memoryCommandFixture(t *testing.T) (*Channel, *sqlite.Store, *[]string) {
	t.Helper()
	s, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err = s.RunMigrations(context.Background()); err != nil {
		t.Fatal(err)
	}
	var texts []string
	b, err := bot.New("123:fixture", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, &http.Client{Transport: presentationTransport(func(r *http.Request) (*http.Response, error) {
		if parseErr := r.ParseMultipartForm(1 << 20); parseErr != nil {
			t.Error(parseErr)
		}
		if !strings.HasSuffix(r.URL.Path, "/sendMessage") || r.FormValue("chat_id") != "42" || r.FormValue("protect_content") != "true" {
			t.Error("unexpected personal delivery")
		}
		texts = append(texts, r.FormValue("text"))
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":1}}`)), Header: http.Header{}}, nil
	})}))
	if err != nil {
		t.Fatal(err)
	}
	c := &Channel{bot: b, botID: 7, instanceID: "test", username: "FixtureBot", store: s, logger: zap.NewNop(), allowedIDs: map[int64]struct{}{42: {}}, commands: make(map[string]CommandFunc)}
	c.RegisterStatusCommand()
	return c, s, &texts
}

func memoryCommandMessage(t *testing.T, c *Channel, thread int) (*models.Message, channel.IncomingMessage) {
	t.Helper()
	m := &models.Message{Text: "/status", Chat: models.Chat{ID: 42, Type: models.ChatTypePrivate}, From: &models.User{ID: 42}, MessageThreadID: thread}
	key := c.routeKey(42, thread)
	if err := c.persistRoute(context.Background(), key, m, "canonical-owner"); err != nil {
		t.Fatal(err)
	}
	return m, channel.IncomingMessage{ChatID: key, UserID: "42", ResolvedUserID: "canonical-owner", ProvenPrivateRoute: true, LanguageCode: "en"}
}

func TestStatusCountsAccountMemoryAcrossRootAndTopics(t *testing.T) {
	c, s, texts := memoryCommandFixture(t)
	ctx := context.Background()
	for i := 0; i < 151; i++ {
		chat := ""
		if i >= 144 {
			chat = "42"
		}
		if err := s.SaveInsight(ctx, &domain.Insight{UserID: "canonical-owner", ChatID: chat, Content: "insight"}); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-time.Hour)
	for _, insight := range []*domain.Insight{{UserID: "another-owner", Content: "other"}, {UserID: "canonical-owner", Content: "expired", ExpiresAt: &past}} {
		if err := s.SaveInsight(ctx, insight); err != nil {
			t.Fatal(err)
		}
	}
	for _, owner := range []string{"canonical-owner", "canonical-owner", "another-owner"} {
		if err := s.SaveFact(ctx, &domain.Fact{UserID: owner, Content: "remembered"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, thread := range []int{0, 77} {
		m, msg := memoryCommandMessage(t, c, thread)
		if err := s.SaveMessage(ctx, &domain.ChatMessage{ChatID: msg.ChatID, UserID: msg.ResolvedUserID, Role: domain.RoleUser, Content: "turn"}); err != nil {
			t.Fatal(err)
		}
		for _, status := range []string{"pending", "done"} {
			if err := s.CreateReminder(ctx, &domain.Reminder{ChatID: msg.ChatID, UserID: msg.ResolvedUserID, Title: "reminder", Status: status, DueAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
		}
		if !c.handleRegisteredCommand(ctx, m, msg) {
			t.Fatal("real command boundary missed status")
		}
		got := (*texts)[len(*texts)-1]
		for _, want := range []string{"Account memory", "Facts: 2", "Active insights: 151", "Stored messages: 1", "Pending reminders: 1"} {
			if !strings.Contains(got, want) {
				t.Fatalf("missing %q: %s", want, got)
			}
		}
		if strings.Contains(got, "Tasks:") || (thread == 0 && !strings.Contains(got, "This chat")) || (thread > 0 && !strings.Contains(got, "This topic")) {
			t.Fatal(got)
		}
	}
	// Saved channel locale wins over the Telegram UI language.
	m, msg := memoryCommandMessage(t, c, 77)
	msg.Locale = "ru"
	c.handleRegisteredCommand(ctx, m, msg)
	if !strings.Contains((*texts)[2], "Активные инсайты: 151") {
		t.Fatal((*texts)[2])
	}
}

type statusReadStore struct {
	*sqlite.Store
	before func()
	reads  int
}

func (s *statusReadStore) CountFactsByUser(context.Context, string) (int, error) {
	s.reads++
	if s.before != nil {
		s.before()
	}
	return 0, errors.New("fixture failure")
}

func TestStatusDeniesUnprovenPersonalRoutesBeforeReads(t *testing.T) {
	for _, scenario := range []string{"missing owner", "external owner", "group", "wrong actor", "missing private proof", "wrong native route", "foreign instance", "foreign bot", "paused", "deleted", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			c, s, texts := memoryCommandFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			m, msg := memoryCommandMessage(t, c, 77)
			switch scenario {
			case "missing owner":
				msg.ResolvedUserID = ""
			case "external owner":
				msg.ResolvedUserID = "42"
			case "group":
				m.Chat.Type = models.ChatTypeGroup
			case "wrong actor":
				m.From.ID = 43
			case "missing private proof":
				msg.ProvenPrivateRoute = false
			case "wrong native route":
				m.MessageThreadID = 78
			case "foreign instance":
				c.instanceID = "foreign"
			case "foreign bot":
				c.botID = 8
			case "paused", "deleted":
				if err := s.PutTelegramTopicState(ctx, &domain.TelegramTopicState{ConversationID: msg.ChatID, Status: scenario}); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				cancel()
			}
			spy := &statusReadStore{Store: s}
			c.store = spy
			if !c.handleRegisteredCommand(ctx, m, msg) {
				t.Fatal("status fell through")
			}
			if spy.reads != 0 || len(*texts) != 0 {
				t.Fatal("unproven owner read or received personal counts")
			}
		})
	}
}

func TestStatusReadFailureAndLifecycleChangeBeforeDelivery(t *testing.T) {
	for _, state := range []string{"", "paused", "deleted", "cancel"} {
		t.Run(state, func(t *testing.T) {
			c, s, texts := memoryCommandFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			m, msg := memoryCommandMessage(t, c, 77)
			spy := &statusReadStore{Store: s, before: func() {
				if state == "cancel" {
					cancel()
				} else if state != "" {
					if err := s.PutTelegramTopicState(ctx, &domain.TelegramTopicState{ConversationID: msg.ChatID, Status: state}); err != nil {
						t.Error(err)
					}
				}
			}}
			c.store = spy
			c.handleRegisteredCommand(ctx, m, msg)
			if state != "" {
				if len(*texts) != 0 {
					t.Fatal("personal reply escaped final route guard")
				}
				return
			}
			if len(*texts) != 1 || !strings.Contains((*texts)[0], "Facts: unavailable") || !strings.Contains((*texts)[0], "Active insights: 0") {
				t.Fatal(*texts)
			}
		})
	}
}

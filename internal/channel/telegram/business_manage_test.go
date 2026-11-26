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
	"github.com/iulita-ai/iulita/internal/storage"
	business "github.com/iulita-ai/iulita/internal/telegrambusiness"
)

type businessManageStore struct {
	storage.Repository
	calls int
	actor int64
	owner string
}

func (s *businessManageStore) ResolveTelegramBusinessOwner(_ context.Context, instance string, actor int64) (string, error) {
	s.calls++
	if instance != "manage-instance" || actor != s.actor {
		return "", errors.New("denied")
	}
	return s.owner, nil
}

type businessManageRepo struct {
	business.Repository
	states               []business.State
	reads, writes, lists int
}

func (r *businessManageRepo) List(_ context.Context, owner string) ([]business.State, error) {
	r.lists++
	var out []business.State
	for index := range r.states {
		s := &r.states[index]
		if s.Connection.OwnerID == owner {
			out = append(out, *s)
		}
	}
	return out, nil
}
func (r *businessManageRepo) Get(context.Context, business.Key) (*business.State, error) {
	r.reads++
	return nil, errors.New("private data must not be queried")
}
func (r *businessManageRepo) Update(context.Context, business.Key, func(*business.State) error) error {
	r.writes++
	return errors.New("no writes")
}

func TestBusinessManagementPayloadIsOwnerNavigationOnly(t *testing.T) {
	for _, test := range []struct {
		name, text, chatType, owner, language string
		actor, chat, knownBot                 int64
		allowed, known                        bool
		replies                               int
	}{
		{"known owner English", "/start bizChat303", "private", "owner", "en", 42, 42, 123, true, true, 1},
		{"known owner Russian", "/start bizChat303", "private", "owner", "ru", 42, 42, 123, true, true, 1},
		{"forged peer", "/start bizChat404", "private", "owner", "en", 42, 42, 123, true, true, 0},
		{"wrong actor", "/start bizChat303", "private", "owner", "en", 99, 42, 123, true, true, 0},
		{"not allowed", "/start bizChat303", "private", "owner", "en", 42, 42, 123, false, true, 0},
		{"group command", "/start bizChat303", "supergroup", "owner", "en", 42, 42, 123, true, true, 0},
		{"unknown owner", "/start bizChat303", "private", "", "en", 42, 42, 123, true, true, 0},
		{"wrong numeric bot", "/start bizChat303", "private", "owner", "en", 42, 42, 999, true, true, 0},
		{"unknown peer", "/start bizChat303", "private", "owner", "en", 42, 42, 123, true, false, 0},
		{"extra parameters", "/start bizChat303 send", "private", "owner", "en", 42, 42, 123, true, true, 0},
		{"invalid target", "/start bizChat-303", "private", "owner", "en", 42, 42, 123, true, true, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			b, err := bot.New("123:fixture", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, &http.Client{Transport: businessRoundTrip(func(req *http.Request) (*http.Response, error) {
				calls++
				if err := req.ParseMultipartForm(1 << 20); err != nil {
					t.Error(err)
				}
				if !strings.HasSuffix(req.URL.Path, "/sendMessage") || req.FormValue("chat_id") != "42" || req.FormValue("business_connection_id") != "" || req.FormValue("protect_content") != "true" {
					t.Error("unexpected recipient or account operation")
				}
				text := req.FormValue("text")
				if !strings.Contains(text, "/secretary") || strings.Contains(text, "303") || strings.Contains(text, "private body") {
					t.Error("navigation disclosed peer data")
				}
				if test.language == "ru" && !strings.Contains(text, "Секретарь Telegram") {
					t.Error("missing Russian navigation")
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":1}}`))}, nil
			})}))
			if err != nil {
				t.Fatal(err)
			}
			store := &businessManageStore{actor: 42, owner: test.owner}
			state := business.State{Connection: business.Connection{RemoteConnection: business.RemoteConnection{Key: business.Key{InstanceID: "manage-instance", BotID: test.knownBot, ConnectionID: "connection"}, OwnerTelegramID: 42}, OwnerID: "owner"}, Peers: map[int64]*business.Peer{}}
			if test.known {
				state.Peers[303] = &business.Peer{ID: 303, Messages: []business.Message{{Text: "private body"}}}
			}
			repo := &businessManageRepo{states: []business.State{state}}
			allowed := int64(42)
			if !test.allowed {
				allowed = 7
			}
			c := &Channel{bot: b, botID: 123, instanceID: "manage-instance", store: store, business: business.New(repo, nil, nil, business.Options{}), allowedIDs: map[int64]struct{}{allowed: {}}}
			msg := &models.Message{Text: test.text, Chat: models.Chat{ID: test.chat, Type: models.ChatType(test.chatType)}, From: &models.User{ID: test.actor, LanguageCode: test.language}}
			if !c.handleBusinessManage(context.Background(), msg) {
				t.Error("business start escaped to ordinary handlers")
			}
			if calls != test.replies || repo.reads != 0 || repo.writes != 0 {
				t.Fatalf("replies=%d privateReads=%d writes=%d", calls, repo.reads, repo.writes)
			}
		})
	}
}
func TestUnrelatedStartDoesNotEnterBusinessManagement(t *testing.T) {
	c := &Channel{}
	for _, text := range []string{"/start", "/help", "plain text", "/start unrelated"} {
		if c.handleBusinessManage(context.Background(), &models.Message{Text: text}) {
			t.Fatalf("handled %q", text)
		}
	}
}

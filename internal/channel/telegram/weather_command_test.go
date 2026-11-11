package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/defaults"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/i18n"
	weatherSkill "github.com/iulita-ai/iulita/internal/skill/weather"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
	"go.uber.org/zap"
	"golang.org/x/text/language"
)

func TestWeatherCommandLocationSelectionAndAdmission(t *testing.T) {
	for _, tt := range []struct {
		name, command, source, wantPlace                             string
		days                                                         int
		widgets, lookupError, deleteDuringLookup, cancelDuringLookup bool
		realMemory, fetchError, disableDuringLookup, widgetFailure   bool
		wantLookups, wantFetch                                       int
	}{
		{name: "saved city", command: "/weather", source: "memory", wantPlace: "Berlin", days: 4, wantLookups: 1, wantFetch: 1, realMemory: true},
		{name: "explicit overrides memory", command: "/weather Paris 3", wantPlace: "Paris", days: 3, wantFetch: 1},
		{name: "explicit country qualifier", command: "/weather Berlin (Germany)", wantPlace: "Berlin, Germany", days: 4, wantFetch: 1},
		{name: "explicit invariant city Kale", command: "/weather Кале", wantPlace: "Кале", days: 4, wantFetch: 1},
		{name: "explicit invariant city Udine", command: "/weather Удине", wantPlace: "Удине", days: 4, wantFetch: 1},
		{name: "unsafe explicit place", command: "/weather Paris token=fixture"},
		{name: "unsafe resolved place", command: "/weather", source: "memory", wantPlace: "Paris token=fixture", wantLookups: 1},
		{name: "day only", command: "/weather 7", source: "memory", wantPlace: "Berlin", days: 7, wantLookups: 1, wantFetch: 1},
		{name: "text day only", command: "/weather text 7", source: "memory", wantPlace: "Berlin", days: 7, widgets: true, wantLookups: 1, wantFetch: 1},
		{name: "widget default", command: "/weather", source: "default", wantPlace: defaults.City, days: 4, widgets: true, wantLookups: 1, wantFetch: 1},
		{name: "shared place", command: "/weather", source: "shared", wantPlace: "52.500000, 13.400000", days: 4, wantLookups: 1, wantFetch: 1},
		{name: "zero days", command: "/weather 0"},
		{name: "too many days", command: "/weather text Paris 17"},
		{name: "day integer overflow", command: "/weather 9999999999999999999999999"},
		{name: "lookup error", command: "/weather", lookupError: true, wantLookups: 1},
		{name: "provider error preserves selection", command: "/weather", source: "memory", wantPlace: "Berlin", days: 4, wantLookups: 1, wantFetch: 1, fetchError: true},
		{name: "widget fallback note once", command: "/weather", source: "memory", wantPlace: "Berlin", days: 4, wantLookups: 1, wantFetch: 1, widgets: true, widgetFailure: true},
		{name: "deleted during lookup", command: "/weather", deleteDuringLookup: true, wantLookups: 1},
		{name: "canceled during lookup", command: "/weather", cancelDuringLookup: true, wantLookups: 1},
		{name: "disabled during lookup", command: "/weather", disableDuringLookup: true, wantLookups: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s, err := sqlite.New(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err = s.RunMigrations(ctx); err != nil {
				t.Fatal(err)
			}
			var texts, captions []string
			b, err := bot.New("123:fixture", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, &http.Client{Transport: presentationTransport(func(r *http.Request) (*http.Response, error) {
				_ = r.ParseMultipartForm(1 << 20)
				if r.FormValue("message_thread_id") != "77" {
					t.Error("reply left original topic")
				}
				if strings.HasSuffix(r.URL.Path, "sendWidget") {
					captions = append(captions, r.FormValue("caption"))
					if tt.widgetFailure {
						return &http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"ok":false,"error_code":400,"description":"Bad Request: widgets unavailable"}`))}, nil
					}
				} else {
					texts = append(texts, r.FormValue("text"))
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":9,"chat":{"id":42,"type":"private"}}}`))}, nil
			})}))
			if err != nil {
				t.Fatal(err)
			}
			c := &Channel{bot: b, botID: 7, instanceID: "a", store: s, userResolver: dayTestResolver{}, logger: zap.NewNop(), sentMsgs: newSentTracker()}
			conversation := c.routeKey(42, 77)
			if err = s.PutTelegramRoute(ctx, &domain.TelegramConversationRoute{BotID: 7, InstanceID: "a", ConversationID: conversation, ChatID: 42, ThreadID: 77, Private: true, OwnerID: "alice"}); err != nil {
				t.Fatal(err)
			}
			lookups, fetches := 0, 0
			enabled := true
			c.weather = &WeatherController{Widgets: tt.widgets, Enabled: func() bool { return enabled }, ResolveLocation: func(work context.Context, owner, chat string) (weatherSkill.CommandLocation, error) {
				lookups++
				if owner != "alice" || chat != conversation {
					t.Error("noncanonical identity in memory lookup")
				}
				if tt.lookupError {
					return weatherSkill.CommandLocation{}, errors.New("fixture lookup error")
				}
				if tt.deleteDuringLookup {
					if e := s.PutTelegramTopicState(work, &domain.TelegramTopicState{ConversationID: conversation, Status: "deleted"}); e != nil {
						t.Error(e)
					}
				}
				if tt.cancelDuringLookup {
					cancel()
				}
				if tt.disableDuringLookup {
					enabled = false
				}
				return weatherSkill.CommandLocation{Location: tt.wantPlace, Source: tt.source}, nil
			}, Fetch: func(work context.Context, location string, days int) (*domain.WeatherCard, error) {
				fetches++
				if work.Err() != nil || location != tt.wantPlace || days != tt.days {
					t.Errorf("unexpected fetch: %q %d %v", location, days, work.Err())
				}
				if tt.fetchError {
					return nil, errors.New("fixture provider failure")
				}
				card := weatherFixture(days)
				card.Location = "Selected city"
				return card, nil
			}}
			if tt.realMemory {
				if err = s.CreateUser(ctx, &domain.User{ID: "alice", Username: "alice"}); err != nil {
					t.Fatal(err)
				}
				if err = s.SaveFact(ctx, &domain.Fact{ChatID: "other-topic", UserID: "alice", SourceType: "user", Content: "User lives in Berlin", CreatedAt: time.Now().Add(-time.Hour)}); err != nil {
					t.Fatal(err)
				}
				resolver := weatherSkill.New(s, nil, nil)
				c.weather.ResolveLocation = func(work context.Context, owner, chat string) (weatherSkill.CommandLocation, error) {
					lookups++
					return resolver.ResolveCommandLocation(work, owner, chat)
				}
			}
			m := &models.Message{Text: tt.command, Chat: models.Chat{ID: 42, Type: models.ChatTypePrivate}, From: &models.User{ID: 42, LanguageCode: "en"}, MessageThreadID: 77}
			if !c.handleWeatherCommand(ctx, m) {
				t.Fatal("command unhandled")
			}
			c.wg.Wait()
			if lookups != tt.wantLookups || fetches != tt.wantFetch {
				t.Fatalf("lookups %d fetches %d", lookups, fetches)
			}
			if strings.Contains(strings.Join(texts, "\n"), "token=fixture") {
				t.Fatal("unsafe place echoed in response")
			}
			if tt.deleteDuringLookup || tt.cancelDuringLookup || tt.disableDuringLookup {
				if len(texts)+len(captions) != 0 {
					t.Fatal("unavailable conversation received response")
				}
				return
			}
			if tt.fetchError {
				return
			}
			if tt.wantFetch == 1 {
				if len(texts) == 0 {
					t.Fatal("forecast text missing")
				}
				note := weatherLocationNote("en", tt.source)
				if note != "" && !strings.Contains(strings.Join(texts, "\n"), note) {
					t.Fatal("location source missing from text")
				}
				if note != "" && strings.Count(strings.Join(texts, "\n"), note) != 1 {
					t.Fatal("source note duplicated")
				}
				if strings.Count(strings.Join(texts, "\n"), "Weather · Selected city") != 1 {
					t.Fatal("forecast duplicated or missing")
				}
				if tt.widgets && !strings.Contains(tt.command, " text") && (len(captions) == 0 || !strings.Contains(captions[0], note)) {
					t.Fatal("source missing from widget caption")
				}
				if strings.Contains(tt.command, " text") && len(captions) != 0 {
					t.Fatal("text mode emitted widget")
				}
			}
		})
	}
}

func TestWeatherSnapshotPrecedesLaterClear(t *testing.T) {
	c, s, _ := memoryCommandFixture(t)
	ctx := context.Background()
	c.userResolver = commandOwnerResolver{}
	c.topicsEnabled = true
	m, msg := memoryCommandMessage(t, c, 77)
	m.Text = "/weather"
	if err := s.SaveMessage(ctx, &domain.ChatMessage{ChatID: msg.ChatID, UserID: msg.ResolvedUserID, Role: domain.RoleUser, Content: "Я сейчас в Москве", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	started, release, cleared := make(chan struct{}), make(chan struct{}), make(chan struct{})
	got := make(chan string, 1)
	resolver := weatherSkill.New(s, nil, nil)
	c.weather = &WeatherController{ResolveLocation: func(work context.Context, owner, conversation string) (weatherSkill.CommandLocation, error) {
		close(started)
		<-release
		return resolver.ResolveCommandLocation(work, owner, conversation)
	}, Fetch: func(_ context.Context, place string, days int) (*domain.WeatherCard, error) {
		got <- place
		return weatherFixture(days), nil
	}}
	d := newDebouncer(0, func(channel.IncomingMessage) {})
	if !c.queueWeatherCommand(ctx, m, msg.ChatID, d) {
		t.Fatal("weather not queued")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("snapshot did not start")
	}
	d.control(msg.ChatID, true, func() {
		if err := s.ClearHistory(ctx, msg.ChatID); err != nil {
			t.Error(err)
		}
		close(cleared)
	})
	// A blocked snapshot must keep its own conversation lane occupied. Provider
	// HTTP remains outside this lane; only the bounded local snapshot is ordered.
	select {
	case <-cleared:
		close(release)
		d.flushAll()
		c.wg.Wait()
		t.Fatal("clear overtook weather snapshot")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	d.flushAll()
	c.wg.Wait()
	select {
	case place := <-got:
		if place != "Москва" {
			t.Fatal(place)
		}
	default:
		t.Fatal("missing forecast")
	}
	if n, err := s.CountMessages(ctx, msg.ChatID); err != nil || n != 0 {
		t.Fatalf("later clear did not run: %d %v", n, err)
	}
}

func TestForwardedLocationCannotBecomeOwnerAssertion(t *testing.T) {
	if err := i18n.Init(); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"text", "location", "automatic"} {
		t.Run(kind, func(t *testing.T) {
			c, s, _ := memoryCommandFixture(t)
			ctx := context.Background()
			m, msg := memoryCommandMessage(t, c, 77)
			if err := s.CreateUser(ctx, &domain.User{ID: msg.ResolvedUserID, Username: "owner"}); err != nil {
				t.Fatal(err)
			}
			if err := s.SaveFact(ctx, &domain.Fact{UserID: msg.ResolvedUserID, SourceType: "user", Content: "I live in Berlin", CreatedAt: time.Now().Add(-time.Hour)}); err != nil {
				t.Fatal(err)
			}
			m.Text = "I am in Paris"
			m.ForwardOrigin = &models.MessageOrigin{Type: models.MessageOriginTypeUser, MessageOriginUser: &models.MessageOriginUser{Type: models.MessageOriginTypeUser, Date: 1, SenderUser: models.User{ID: 99}}}
			if kind == "automatic" {
				m.ForwardOrigin = nil
				m.IsAutomaticForward = true
			}
			if kind == "location" {
				m.Text = ""
				m.Location = &models.Location{Latitude: 48.85, Longitude: 2.35}
			}
			text, _ := normalizedTelegramContent(m)
			if loc := locationFromMessage(m); loc != nil {
				text += "\n" + formatLocationText(language.English, loc)
			}
			if err := s.SaveMessage(ctx, &domain.ChatMessage{ChatID: msg.ChatID, UserID: msg.ResolvedUserID, Role: domain.RoleUser, Content: text, CreatedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			got, err := weatherSkill.New(s, nil, nil).ResolveCommandLocation(ctx, msg.ResolvedUserID, msg.ChatID)
			if err != nil || got.Location != "Berlin" || got.Source != "memory" {
				t.Fatalf("forward promoted to owner location: %+v %v", got, err)
			}
		})
	}
}

func TestDisabledWeatherGuardStopsRemainingPagesAndChunks(t *testing.T) {
	for _, mode := range []string{"text", "widgets"} {
		t.Run(mode, func(t *testing.T) {
			c, _, _ := memoryCommandFixture(t)
			ctx := context.Background()
			m, msg := memoryCommandMessage(t, c, 77)
			enabled, calls := true, 0
			var lastText string
			b, err := bot.New("123:fixture", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, &http.Client{Transport: presentationTransport(func(r *http.Request) (*http.Response, error) {
				_ = r.ParseMultipartForm(1 << 20)
				calls++
				lastText = r.FormValue("text")
				enabled = false
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":1}}`)), Header: http.Header{}}, nil
			})}))
			if err != nil {
				t.Fatal(err)
			}
			c.bot = b
			c.weather = &WeatherController{Widgets: true, Enabled: func() bool { return enabled }}
			guarded := weatherSkill.WithCommandGuard(ctx, func(context.Context) error {
				if !enabled {
					return context.Canceled
				}
				return nil
			})
			if mode == "text" {
				c.sendWeatherText(guarded, msg.ChatID, msg.ResolvedUserID, strings.Repeat("x", 9000))
			} else {
				c.sendWeather(guarded, msg.ChatID, msg.ResolvedUserID, "en", weatherFixture(16), "")
			}
			if calls != 1 {
				t.Fatalf("delivery continued after disable: %d", calls)
			}
			// A weather-only guard must not disable unrelated account status.
			if !c.handleRegisteredCommand(ctx, m, msg) || calls != 2 || !strings.Contains(lastText, "Active insights: 0") {
				t.Fatal("weather disable suppressed status")
			}
		})
	}
}

type commandOwnerResolver struct{}

func (commandOwnerResolver) ResolveUser(context.Context, string, string, string, string) (string, error) {
	return "canonical-owner", nil
}

func TestWeatherIngressWaitsForPriorTopicText(t *testing.T) {
	for _, mode := range []string{"buffered", "active", "preparing"} {
		t.Run(mode, func(t *testing.T) {
			c, s, texts := memoryCommandFixture(t)
			c.topicsEnabled = true
			c.userResolver = commandOwnerResolver{}
			ctx := context.Background()
			m, msg := memoryCommandMessage(t, c, 77)
			m.Text = "/weather"
			got := make(chan string, 1)
			c.weather = &WeatherController{ResolveLocation: weatherSkill.New(s, nil, nil).ResolveCommandLocation, Fetch: func(_ context.Context, place string, days int) (*domain.WeatherCard, error) {
				got <- place
				return weatherFixture(days), nil
			}}
			started, release := make(chan struct{}), make(chan struct{})
			window := time.Hour
			if mode == "active" {
				window = 0
			}
			d := newDebouncer(window, func(turn channel.IncomingMessage) {
				if mode == "active" {
					close(started)
					<-release
				}
				if err := s.SaveMessage(ctx, &domain.ChatMessage{ChatID: turn.ChatID, UserID: turn.ResolvedUserID, Role: domain.RoleUser, Content: turn.Text, CreatedAt: time.Now()}); err != nil {
					t.Error(err)
				}
			})
			turn := msg
			turn.Text = "Я сейчас в Москве"
			if mode == "preparing" {
				d.control("topic-prepare:42", false, func() { d.add(turn) })
			} else {
				d.add(turn)
			}
			if mode == "active" {
				select {
				case <-started:
				case <-time.After(time.Second):
					t.Fatal("prior turn did not start")
				}
			}
			if !c.queueWeatherCommand(ctx, m, msg.ChatID, d) {
				t.Fatal("weather not queued")
			}
			// Another conversation/callback lane does not wait for an active turn.
			other := make(chan struct{})
			d.control("other-topic", false, func() { close(other) })
			select {
			case <-other:
			case <-time.After(time.Second):
				t.Fatal("weather blocked dispatcher")
			}
			if mode == "active" {
				close(release)
			}
			d.flushAll()
			c.wg.Wait()
			select {
			case place := <-got:
				if place != "Москва" {
					t.Fatal(place)
				}
			default:
				t.Fatal("no forecast for accepted topic text")
			}
			if len(*texts) != 1 || !strings.Contains((*texts)[0], weatherLocationNote("en", "context")) {
				t.Fatal(*texts)
			}
		})
	}
}

func TestWeatherCommandUsesActualSharedLocationMarkers(t *testing.T) {
	if err := i18n.Init(); err != nil {
		t.Fatal(err)
	}
	s, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err = s.RunMigrations(ctx); err != nil {
		t.Fatal(err)
	}
	for _, tag := range i18n.SupportedLanguages {
		for variant, loc := range []channel.LocationAttachment{
			{Latitude: 52.5, Longitude: 13.4},
			{Latitude: 52.5, Longitude: 13.4, Accuracy: 35},
			{Latitude: 52.5, Longitude: 13.4, Live: true},
			{Latitude: 52.5, Longitude: 13.4, Accuracy: 35, Live: true},
			{Latitude: 52.5, Longitude: 13.4, Title: "Gate", Address: "Main street"},
		} {
			conversation := fmt.Sprintf("pin-%s-%d", tag, variant)
			if err = s.SaveMessage(ctx, &domain.ChatMessage{ChatID: conversation, UserID: "alice", Role: domain.RoleUser, Content: formatLocationText(tag, &loc), CreatedAt: time.Now().Add(-time.Minute)}); err != nil {
				t.Fatal(err)
			}
			got, e := weatherSkill.New(s, nil, nil).ResolveCommandLocation(ctx, "alice", conversation)
			if e != nil || got.Source != "shared" || got.Location != "52.500000, 13.400000" {
				t.Fatalf("%s: %+v %v", tag, got, e)
			}
		}
	}
}

package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
	"go.uber.org/zap"
)

func weatherFixture(days int) *domain.WeatherCard {
	zero := 0.0
	c := &domain.WeatherCard{Location: strings.Repeat("Длинное название города ", 6), Source: "Open-Meteo", RetrievedAt: time.Now(), RequestedDays: days}
	for i := 0; i < days; i++ {
		c.Days = append(c.Days, domain.WeatherDay{Date: time.Date(2026, 11, 10+i, 0, 0, 0, 0, time.UTC).Format("2006-01-02"), TempMinC: &zero, TempMaxC: &zero, Condition: domain.WeatherUnknown})
	}
	return c
}

func TestWeatherProjectionPreservesEveryDayAndZero(t *testing.T) {
	c := weatherFixture(16)
	for _, locale := range []string{"en", "ru"} {
		pages, e := weatherProjection(c, locale)
		if e != nil {
			t.Fatal(e)
		}
		dates := make([]string, 0, len(c.Days))
		for _, raw := range pages {
			if len(raw) > 1024 {
				t.Fatal("state budget")
			}
			var s weatherState
			if e = json.Unmarshal([]byte(raw), &s); e != nil {
				t.Fatal(e)
			}
			if s.Period == "Now" || s.Period == "Сейчас" {
				t.Fatal("forecast mislabeled current")
			}
			if s.Kind != "unknown" || s.Temperature != "0° / 0°" {
				t.Fatal("missing condition or real zero lost")
			}
			for _, d := range s.Days {
				dates = append(dates, d.Day)
			}
		}
		if len(dates) != 16 || dates[0] != "10.11" || dates[15] != "25.11" {
			t.Fatalf("days lost: %v", dates)
		}
	}
	c.Current = &domain.WeatherCurrent{TemperatureC: new(float64), Condition: domain.WeatherClear}
	pages, e := weatherProjection(c, "ru")
	if e != nil {
		t.Fatal(e)
	}
	var s weatherState
	_ = json.Unmarshal([]byte(pages[0]), &s)
	if s.Temperature != "0°" || len(s.Metrics) != 0 {
		t.Fatal("nil metrics rendered as measurements")
	}
}

func TestWeatherDeliveryRechecksOwnerAndStopsAfterDeletion(t *testing.T) {
	ctx := context.Background()
	s, e := sqlite.New(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.RunMigrations(ctx); e != nil {
		t.Fatal(e)
	}
	route := &domain.TelegramConversationRoute{BotID: 7, ConversationID: "weather-test", InstanceID: "a", ChatID: 42, ThreadID: 77, OwnerID: "alice", Private: true}
	if e = s.PutTelegramRoute(ctx, route); e != nil {
		t.Fatal(e)
	}
	calls := 0
	b, e := bot.New("123:fixture", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, &http.Client{Transport: presentationTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if !strings.HasSuffix(r.URL.Path, "sendWidget") {
			t.Fatal("sent after route unavailable")
		}
		_ = r.ParseMultipartForm(1 << 20)
		if r.FormValue("public_data") != "" || len(r.FormValue("chat_data")) > 1024 || r.FormValue("protect_content") != "true" {
			t.Fatal("weather privacy/budget")
		}
		if stateErr := s.PutTelegramTopicState(ctx, &domain.TelegramTopicState{ConversationID: route.ConversationID, Status: "deleted"}); stateErr != nil {
			t.Fatal(stateErr)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":1}}`))}, nil
	})}))
	if e != nil {
		t.Fatal(e)
	}
	c := &Channel{bot: b, botID: 7, instanceID: "a", store: s, logger: zap.NewNop(), weather: &WeatherController{Widgets: true}}
	if c.sendWeather(ctx, route.ConversationID, "bob", "en", weatherFixture(16), "answer") && calls != 0 {
		t.Fatal("foreign owner sent")
	}
	if !c.sendWeather(ctx, route.ConversationID, "alice", "en", weatherFixture(16), "answer") || calls != 1 {
		t.Fatalf("deletion did not fence later sends: %d", calls)
	}
}

func TestWeatherTextRechecksRouteBetweenChunksAndSecretPreflight(t *testing.T) {
	ctx := context.Background()
	s, e := sqlite.New(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.RunMigrations(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.PutTelegramRoute(ctx, &domain.TelegramConversationRoute{BotID: 7, ConversationID: "weather-text", InstanceID: "a", ChatID: 42, OwnerID: "alice", Private: true}); e != nil {
		t.Fatal(e)
	}
	calls := 0
	b, e := bot.New("123:fixture", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, &http.Client{Transport: presentationTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if stateErr := s.PutTelegramTopicState(ctx, &domain.TelegramTopicState{ConversationID: "weather-text", Status: "deleted"}); stateErr != nil {
			t.Fatal(stateErr)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":1}}`))}, nil
	})}))
	if e != nil {
		t.Fatal(e)
	}
	c := &Channel{bot: b, botID: 7, instanceID: "a", store: s, logger: zap.NewNop(), weather: &WeatherController{Widgets: true}}
	for _, secret := range []string{"api_key: sk-abcdef123456", "card 4111 1111 1111 1111"} { // gitleaks:allow -- Synthetic classifier fixtures, never real credentials.
		c.sendWeather(ctx, "weather-text", "alice", "en", weatherFixture(4), secret)
	}
	if calls != 0 {
		t.Fatal("widget sent before full answer secret classification")
	}
	c.sendWeatherText(ctx, "weather-text", "alice", strings.Repeat("forecast text ", 1200))
	if calls != 1 {
		t.Fatalf("later chunks escaped deletion guard: %d", calls)
	}
}

func TestWeatherUnavailableWidgetPreservesFullForecastAndAnswer(t *testing.T) {
	for _, mode := range []string{"budget", "api"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s, e := sqlite.New(":memory:")
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			if e = s.RunMigrations(ctx); e != nil {
				t.Fatal(e)
			}
			if e = s.PutTelegramRoute(ctx, &domain.TelegramConversationRoute{BotID: 7, ConversationID: "weather-fallback", InstanceID: "a", ChatID: 42, OwnerID: "alice", Private: true}); e != nil {
				t.Fatal(e)
			}
			var sent strings.Builder
			widgets := 0
			b, e := bot.New("123:fixture", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, &http.Client{Transport: presentationTransport(func(r *http.Request) (*http.Response, error) {
				_ = r.ParseMultipartForm(1 << 20)
				payload := `{"ok":true,"result":{"message_id":1}}`
				if strings.HasSuffix(r.URL.Path, "sendWidget") {
					widgets++
					payload = `{"ok":false,"error_code":400,"description":"widgets unavailable"}`
				} else {
					sent.WriteString(r.FormValue("text"))
					if r.FormValue("parse_mode") != "HTML" {
						t.Fatal("model companion lost safe formatting")
					}
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(payload))}, nil
			})}))
			if e != nil {
				t.Fatal(e)
			}
			c := &Channel{bot: b, botID: 7, instanceID: "a", store: s, logger: zap.NewNop(), weather: &WeatherController{Widgets: true}}
			if mode == "budget" {
				saved := weatherHTML
				weatherHTML = strings.Repeat("x", 33000)
				defer func() { weatherHTML = saved }()
			}
			if !c.sendWeather(ctx, "weather-fallback", "alice", "en", weatherFixture(16), "Synthesized answer preserved.") {
				t.Fatal("fallback not handled")
			}
			for _, want := range []string{"2026-11-10", "2026-11-25", "Synthesized answer preserved."} {
				if !strings.Contains(sent.String(), want) {
					t.Fatalf("fallback lost %s", want)
				}
			}
			if mode == "api" && widgets != 1 {
				t.Fatal("unexpected widget retry")
			}
			if mode == "budget" && widgets != 0 {
				t.Fatal("oversize template sent")
			}
		})
	}
}

func TestCardTemplateProfileBudgets(t *testing.T) {
	for name, template := range map[string]string{"day": dayHTML, "public": publicWidgetHTML, "weather": weatherHTML} {
		if e := validateWidgetHTML(template); e != nil {
			t.Fatalf("%s: %v", name, e)
		}
		for _, forbidden := range []string{"<iframe", "<image ", "<filter", "<clipPath", "<foreignObject", "text-shadow:", "color-mix(", "https://fonts.", "window.TelegramWidget="} {
			if strings.Contains(template, forbidden) {
				t.Fatalf("%s includes unsupported/external/test-only %s", name, forbidden)
			}
		}
	}
}

func TestFinalResponseSensitivityIncludesWeatherCompanionPayments(t *testing.T) {
	for _, text := range []string{"api_key: sk-abcdef123456", "card 4111 1111 1111 1111"} { // gitleaks:allow (synthetic test fixtures)
		if sensitive, _ := responseSensitivity(text); !sensitive {
			t.Fatal("final reply could bypass weather sensitivity through ordinary delivery")
		}
	}
	if sensitive, _ := responseSensitivity("10.11 · 0° / 12° · Wind 9 km/h · Humidity 62%"); sensitive {
		t.Fatal("ordinary weather classified as a secret")
	}
}

func TestWeatherCompanionFormatRejectionPreservesAllRemainingChunks(t *testing.T) {
	ctx := context.Background()
	s, e := sqlite.New(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.RunMigrations(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.PutTelegramRoute(ctx, &domain.TelegramConversationRoute{BotID: 7, ConversationID: "weather-markdown", InstanceID: "a", ChatID: 42, OwnerID: "alice", Private: true}); e != nil {
		t.Fatal(e)
	}
	calls := 0
	var literal strings.Builder
	b, e := bot.New("123:fixture", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, &http.Client{Transport: presentationTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		_ = r.ParseMultipartForm(1 << 20)
		payload := `{"ok":true,"result":{"message_id":1}}`
		if calls == 1 {
			if r.FormValue("parse_mode") != "HTML" || !strings.Contains(r.FormValue("text"), "<b>Weather</b>") {
				t.Fatal("synthesized answer lost formatting")
			}
			payload = `{"ok":false,"error_code":400,"description":"cannot parse entities"}`
		} else {
			if r.FormValue("parse_mode") != "" {
				t.Fatal("rejected formatting retried")
			}
			literal.WriteString(r.FormValue("text"))
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(payload))}, nil
	})}))
	if e != nil {
		t.Fatal(e)
	}
	c := &Channel{bot: b, botID: 7, instanceID: "a", store: s, logger: zap.NewNop()}
	text := "**Weather**\n" + strings.Repeat("forecast ", 1500)
	c.sendWeatherText(ctx, "weather-markdown", "alice", text, true)
	if literal.String() != text || calls < 3 {
		t.Fatal("format rejection lost remaining forecast chunks")
	}
}

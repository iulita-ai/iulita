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
	"github.com/go-telegram/bot/models"
	"go.uber.org/zap"
)

func TestSharedAndInlineProductionGateNeverLoadsPrivateContext(t *testing.T) {
	if ProductionInlineWidgetsAvailable() {
		t.Fatal("production inline gate opened")
	}
	methods := []string{}
	b, e := bot.New("123:fixture", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, &http.Client{Transport: presentationTransport(func(r *http.Request) (*http.Response, error) {
		methods = append(methods, r.URL.Path)
		if !strings.HasSuffix(r.URL.Path, "/answerWidgetQuery") && !strings.HasSuffix(r.URL.Path, "/answerInlineQuery") {
			t.Fatal("gate attempted a send or private operation")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":true}`)), Header: http.Header{}}, nil
	})}))
	if e != nil {
		t.Fatal(e)
	}
	c := &Channel{bot: b} // No store/resolver: a shared query cannot need private authority.
	var u models.Update
	if e = json.Unmarshal([]byte(`{"update_id":1,"shared_widget_query":{"id":"q","from":{"id":42},"type":{"type":"data"},"data":"d:private-card:1:complete:0","widget_id":"w"}}`), &u); e != nil {
		t.Fatal(e)
	}
	if !c.handleUnavailableWidgetSurface(context.Background(), &u) {
		t.Fatal("shared query dropped")
	}
	if !c.handleUnavailableWidgetSurface(context.Background(), &models.Update{InlineQuery: &models.InlineQuery{ID: "i", From: &models.User{ID: 42}}}) {
		t.Fatal("inline query unhandled")
	}
	if len(methods) != 2 {
		t.Fatalf("unexpected calls %v", methods)
	}
	if c.handleUnavailableWidgetSurface(context.Background(), &models.Update{}) {
		t.Fatal("ordinary update intercepted")
	}
}
func TestDisabledDayCommandExplainsWithoutModel(t *testing.T) {
	sent := 0
	b, e := bot.New("123:fixture", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, &http.Client{Transport: presentationTransport(func(r *http.Request) (*http.Response, error) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(r.URL.Path, "/sendMessage") || r.FormValue("message_thread_id") != "77" || !strings.Contains(r.FormValue("text"), "not enabled") {
			t.Fatal("disabled response lost explanation or route")
		}
		sent++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":1}}`)), Header: http.Header{}}, nil
	})}))
	if e != nil {
		t.Fatal(e)
	}
	c := &Channel{bot: b, username: "OriginalBot", logger: zap.NewNop(), sentMsgs: newSentTracker()}
	if !c.handleDayCommand(context.Background(), &models.Message{Text: "/day", Chat: models.Chat{ID: 42}, MessageThreadID: 77}) || sent != 1 {
		t.Fatal("disabled command forwarded")
	}
}

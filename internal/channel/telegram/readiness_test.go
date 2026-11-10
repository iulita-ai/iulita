package telegram

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"go.uber.org/zap"
)

func TestIdentityReadinessIsReadOnly(t *testing.T) {
	calls := 0
	b, e := bot.New("123:fixture", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, &http.Client{Transport: presentationTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		result := `{"id":42,"is_bot":true,"first_name":"Iulita","username":"BotAI","has_topics_enabled":true}`
		if strings.HasSuffix(r.URL.Path, "/getChat") {
			_ = r.ParseMultipartForm(1 << 20)
			if r.FormValue("chat_id") != "42" {
				t.Fatal("did not inspect numeric self identity")
			}
			result = `{"id":42,"type":"private","active_usernames":["BotAI","OtherAI"]}`
		} else if !strings.HasSuffix(r.URL.Path, "/getMe") {
			t.Fatal("readiness invoked non-read method")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":` + result + `}`)), Header: http.Header{}}, nil
	})}))
	if e != nil {
		t.Fatal(e)
	}
	r, e := InspectBotIdentity(context.Background(), b)
	if e != nil || r.BotID != 42 || !r.TopicsEnabled || len(r.ActiveUsernames) != 2 || calls != 2 {
		t.Fatal(r, e, calls)
	}
}
func TestTestEnvironmentIsConstructorTransportProof(t *testing.T) {
	client := &http.Client{Transport: presentationTransport(func(r *http.Request) (*http.Response, error) {
		if !strings.Contains(r.URL.Path, "/test/") {
			t.Fatal("test bot hit production namespace")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"id":42,"is_bot":true,"first_name":"Test","username":"TestBot"}}`)), Header: http.Header{}}, nil
	})}
	c, e := NewTest("123:fixture", nil, nil, 0, client, zap.NewNop())
	if e != nil || !c.testEnvironment {
		t.Fatal(c, e)
	}
	c.SetInlineWidgetTestMode(true)
	if !c.inlineEnabled() {
		t.Fatal("proven test server unavailable")
	}
	production := &Channel{}
	production.SetInlineWidgetTestMode(true)
	if production.inlineEnabled() {
		t.Fatal("production spoof accepted")
	}
}

package telegram

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot"
)

func TestAliasesRequireSameBotIdentity(t *testing.T) {
	transport := presentationTransport(func(r *http.Request) (*http.Response, error) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		if r.FormValue("chat_id") != "42" {
			t.Fatal("alias resolution used unsupported username lookup")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"id":42,"type":"private","username":"OriginalBot","active_usernames":["OriginalBot","HelpfulAI"]}}`)), Header: http.Header{}}, nil
	})
	b, err := bot.New("123:fixture", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, &http.Client{Transport: transport}))
	if err != nil {
		t.Fatal(err)
	}
	c := &Channel{bot: b, botID: 42, username: "OriginalBot"}
	if err := c.ConfigureAliases(context.Background(), []string{"HelpfulAI"}, "HelpfulAI"); err != nil {
		t.Fatal(err)
	}
	if c.linkUsername() != "HelpfulAI" {
		t.Fatal("preferred alias ignored")
	}
	if cmd, ok := c.commandName("/clear@HelpfulAI"); !ok || cmd != "/clear" {
		t.Fatal("alias command not recognized")
	}
	if _, ok := c.commandName("/clear@ForeignAI"); ok {
		t.Fatal("foreign command accepted")
	}
	if err := c.ConfigureAliases(context.Background(), []string{"ForeignAI"}, "ForeignAI"); err == nil {
		t.Fatal("alias takeover")
	}
	if c.linkUsername() != "HelpfulAI" {
		t.Fatal("failed verification changed identity")
	}
}

func TestCanonicalAliasPromotionKeepsOriginalActiveCommandsWithoutConfig(t *testing.T) {
	b, e := bot.New("123:fixture", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, &http.Client{Transport: presentationTransport(func(r *http.Request) (*http.Response, error) {
		_ = r.ParseMultipartForm(1 << 20)
		if r.FormValue("chat_id") != "42" {
			t.Fatal("identity was not numeric")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"id":42,"type":"private","username":"ExampleAgentAI","active_usernames":["ExampleAgentAI","ExampleOriginalBot","AnotherAliasAI"]}}`)), Header: http.Header{}}, nil
	})}))
	if e != nil {
		t.Fatal(e)
	}
	c := &Channel{bot: b, botID: 42, username: "ExampleAgentAI"}
	if e = c.ConfigureAliases(context.Background(), nil, ""); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"ExampleAgentAI", "ExampleOriginalBot", "AnotherAliasAI"} {
		if cmd, ok := c.commandName("/day@" + name); !ok || cmd != "/day" {
			t.Fatalf("old active name lost: %s", name)
		}
	}
	if c.linkUsername() != "ExampleAgentAI" {
		t.Fatal("canonical fallback was changed")
	}
}

package slack

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/iulita-ai/iulita/internal/channel"
	slackapi "github.com/slack-go/slack"
	"go.uber.org/zap"
)

type presentationTransport func(*http.Request) (*http.Response, error)

func (f presentationTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestVerbatimProcessMessageDeliversLiteralPlainTextOnlyOnce(t *testing.T) {
	text := "  <b>raw</b> &lt;literal&gt; **bold** # heading\n```go\n`code`\n```\n" + strings.Repeat("😀 العربية עברית\n  ", 650) + "\t\n "
	var chunks []string
	transport := presentationTransport(func(req *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(req.URL.Path, "/chat.postMessage") {
			t.Fatal("unexpected Slack API method")
		}
		if err := req.ParseForm(); err != nil {
			t.Fatal(err)
		}
		var blocks []struct {
			Type string `json:"type"`
			Text struct {
				Type  string `json:"type"`
				Text  string `json:"text"`
				Emoji *bool  `json:"emoji"`
			} `json:"text"`
		}
		if err := json.Unmarshal([]byte(req.FormValue("blocks")), &blocks); err != nil {
			t.Fatal(err)
		}
		if len(blocks) != 1 || blocks[0].Type != "section" || blocks[0].Text.Type != "plain_text" || blocks[0].Text.Emoji == nil || *blocks[0].Text.Emoji {
			t.Fatal("literal send included markdown, emoji conversion or bookmark blocks")
		}
		chunk := blocks[0].Text.Text
		if chunk != req.FormValue("text") || !utf8.ValidString(chunk) || len(utf16.Encode([]rune(chunk))) > maxMessageLen {
			t.Fatal("plain-text block/fallback content changed or exceeded limits")
		}
		if req.FormValue("mrkdwn") != "false" || req.FormValue("parse") != "none" || req.FormValue("unfurl_links") != "false" || req.FormValue("unfurl_media") != "false" || req.FormValue("thread_ts") != "10.20" {
			t.Fatal("literal send lost parsing controls or thread routing")
		}
		chunks = append(chunks, chunk)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"ok":true,"channel":"C1","ts":"11.22"}`)), Header: http.Header{}}, nil
	})
	c := &Channel{client: slackapi.New("fixture", slackapi.OptionAPIURL("https://slack.test/api/"), slackapi.OptionHTTPClient(&http.Client{Transport: transport})), logger: zap.NewNop(), chatMetaM: make(map[string]*chatMeta)}
	c.storeChatMeta("slack:C1:U1", &chatMeta{channelID: "C1", userID: "U1", threadTS: "10.20"})
	handlerCalls := 0
	c.processMessage(context.Background(), func(ctx context.Context, msg channel.IncomingMessage) (string, error) {
		handlerCalls++
		derived, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		channel.MarkVerbatimResponse(derived)
		return text, nil
	}, channel.IncomingMessage{ChatID: "slack:C1:U1"})
	if handlerCalls != 1 || len(chunks) < 2 || strings.Join(chunks, "") != text {
		t.Fatal("Slack delivery duplicated, formatted or lost literal text")
	}
}

func TestVerbatimDeliveryFailureDoesNotRetry(t *testing.T) {
	calls := 0
	transport := presentationTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"ok":false,"error":"fixture_failed"}`)), Header: http.Header{}}, nil
	})
	c := &Channel{client: slackapi.New("fixture", slackapi.OptionAPIURL("https://slack.test/api/"), slackapi.OptionHTTPClient(&http.Client{Transport: transport})), logger: zap.NewNop(), chatMetaM: make(map[string]*chatMeta)}
	c.sendVerbatimResponse(context.Background(), "slack:D1", strings.Repeat("a", 9000))
	if calls != 1 {
		t.Fatal("failed literal send retried or sent remaining chunks")
	}
}

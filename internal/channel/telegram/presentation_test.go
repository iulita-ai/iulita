package telegram

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

	"github.com/go-telegram/bot"
	"go.uber.org/zap"
)

type presentationTransport func(*http.Request) (*http.Response, error)

func (f presentationTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestVerbatimDeliveryRequestsPreserveTextIncludingEphemeral(t *testing.T) {
	text := "  <b>Raw HTML & entity &lt;tag&gt;</b>\n```go\nfmt.Println(`a`)\n```\n" +
		strings.Repeat("😀 العربية עברית\n  ", 650) + "\n\t "
	for _, receiver := range []int64{0, 73} {
		t.Run(map[bool]string{false: "regular", true: "ephemeral"}[receiver != 0], func(t *testing.T) {
			var chunks []string
			transport := presentationTransport(func(req *http.Request) (*http.Response, error) {
				if !strings.HasSuffix(req.URL.Path, "/sendMessage") {
					t.Fatal("unexpected bot method")
				}
				if err := req.ParseMultipartForm(1 << 20); err != nil {
					t.Fatal(err)
				}
				chunk := req.FormValue("text")
				if !utf8.ValidString(chunk) || len(utf16.Encode([]rune(chunk))) > 4096 || req.FormValue("parse_mode") != "" || req.FormValue("entities") != "" || req.FormValue("reply_markup") != "" {
					t.Fatal("literal delivery included formatting or exceeded Telegram limits")
				}
				if len(chunks) == 0 && req.FormValue("reply_parameters") == "" {
					t.Fatal("first chunk lost reply threading")
				}
				if len(chunks) > 0 && req.FormValue("reply_parameters") != "" {
					t.Fatal("later chunk duplicated reply threading")
				}
				if receiver != 0 {
					var ephemeral struct {
						ReceiverUserID int64 `json:"receiver_user_id"`
					}
					if json.Unmarshal([]byte(req.FormValue("ephemeral_message_parameters")), &ephemeral) != nil || ephemeral.ReceiverUserID != receiver {
						t.Fatal("ephemeral literal send lost receiver isolation")
					}
				} else if req.FormValue("ephemeral_message_parameters") != "" {
					t.Fatal("ordinary literal send became ephemeral")
				}
				chunks = append(chunks, chunk)
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":123}}`)), Header: http.Header{}}, nil
			})
			b, err := bot.New("123:fixture", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, &http.Client{Transport: transport}))
			if err != nil {
				t.Fatal(err)
			}
			c := &Channel{bot: b, logger: zap.NewNop(), sentMsgs: newSentTracker()}
			c.sendVerbatimResponse(context.Background(), 42, text, 11, receiver)
			if len(chunks) < 2 || strings.Join(chunks, "") != text {
				t.Fatal("outgoing Telegram requests changed the literal response")
			}
		})
	}
}

func TestVerbatimDeliveryFailureDoesNotRetryOrExposeEphemeralPublicly(t *testing.T) {
	calls := 0
	b, err := bot.New("123:fixture", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, &http.Client{Transport: presentationTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"ok":false,"error_code":400,"description":"fixture rejected"}`)), Header: http.Header{}}, nil
	})}))
	if err != nil {
		t.Fatal(err)
	}
	c := &Channel{bot: b, logger: zap.NewNop()}
	c.sendVerbatimResponse(context.Background(), 42, strings.Repeat("a", 9000), 11, 73)
	if calls != 1 {
		t.Fatal("failed ephemeral delivery retried or sent later chunks")
	}
}

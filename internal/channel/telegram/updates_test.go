package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestPollingExplicitlySubscribesToNewAndExistingUpdateKinds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	captured := make(chan []string, 1)
	client := &http.Client{Transport: presentationTransport(func(r *http.Request) (*http.Response, error) {
		result := `{"id":42,"is_bot":true,"first_name":"Fixture","username":"BotAI"}`
		if strings.HasSuffix(r.URL.Path, "/getUpdates") {
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
			}
			var kinds []string
			if err := json.Unmarshal([]byte(r.FormValue("allowed_updates")), &kinds); err != nil {
				t.Errorf("missing or malformed allowed_updates: %v", err)
			}
			select {
			case captured <- kinds:
			default:
			}
			cancel()
			result = `[]`
		} else if !strings.HasSuffix(r.URL.Path, "/getMe") {
			t.Errorf("unexpected method %s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":` + result + `}`)), Header: http.Header{}}, nil
	})}
	c, e := New("123:fixture", nil, nil, 0, client, zap.NewNop())
	if e != nil {
		t.Fatal(e)
	}
	c.bot.Start(ctx)
	var kinds []string
	select {
	case kinds = <-captured:
	default:
		t.Fatal("poll never requested updates")
	}
	present := map[string]bool{}
	for _, kind := range kinds {
		present[kind] = true
	}
	for _, kind := range []string{"message", "edited_message", "callback_query", "message_reaction", "widget_query", "shared_widget_query", "inline_query", "chosen_inline_result", "business_message", "channel_post", "stopped_message_generation"} {
		if !present[kind] {
			t.Errorf("subscription dropped %s", kind)
		}
	}
}

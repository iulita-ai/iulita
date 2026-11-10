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
	"github.com/iulita-ai/iulita/internal/dayview"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
	"go.uber.org/zap"
)

type dayTestResolver struct{}

func (dayTestResolver) ResolveUser(context.Context, string, string, string, string) (string, error) {
	return "alice", nil
}

type dayTestSource struct{}

func (dayTestSource) Name() string { return "todoist" }
func (dayTestSource) List(context.Context, string, time.Time, *time.Location) ([]dayview.Item, error) {
	return []dayview.Item{{ID: "task", Source: "todoist", Title: "card 4111 1111 1111 1111", Revision: "r1", Complete: true}}, nil
}
func (dayTestSource) Change(context.Context, string, dayview.Item, string, time.Time) error {
	return nil
}
func TestDayCardPrivateDataAndQueryBinding(t *testing.T) {
	ctx := context.Background()
	s, e := sqlite.New(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.RunMigrations(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.EnsureDaySchema(ctx); e != nil {
		t.Fatal(e)
	}
	var state dayState
	calls := 0
	b, e := bot.New("123:fixture", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, &http.Client{Transport: presentationTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		_ = r.ParseMultipartForm(1 << 20)
		result := `true`
		if strings.HasSuffix(r.URL.Path, "sendWidget") {
			if r.FormValue("public_data") != "" {
				t.Fatal("personal public_data")
			}
			if strings.Contains(r.FormValue("chat_data"), "4111 1111") {
				t.Fatal("unmasked card")
			}
			if e = json.Unmarshal([]byte(r.FormValue("chat_data")), &state); e != nil {
				t.Fatal(e)
			}
			result = `{"message_id":9,"chat":{"id":42,"type":"private"}}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":` + result + `}`))}, nil
	})}))
	if e != nil {
		t.Fatal(e)
	}
	c := &Channel{bot: b, instanceID: "a", store: s, userResolver: dayTestResolver{}, logger: zap.NewNop(), sentMsgs: newSentTracker()}
	c.day = &DayController{Store: s, Actions: false, Service: func(string) *dayview.Service { return &dayview.Service{Sources: []dayview.Source{dayTestSource{}}} }}
	m := &models.Message{Text: "/day", Chat: models.Chat{ID: 42, Type: models.ChatTypePrivate}, From: &models.User{ID: 42, LanguageCode: "en"}, MessageThreadID: 77}
	c.openDay(ctx, m, "alice")
	if calls != 1 || state.Refresh == "" || len(state.Rows) != 1 || state.Rows[0].Complete != "" {
		t.Fatalf("calls=%d state=%+v", calls, state)
	}
	before := calls
	q := &models.WidgetQuery{ID: "q", From: models.User{ID: 43}, Chat: &models.Chat{ID: 42, Type: models.ChatTypePrivate}, MessageID: 9, Data: state.Refresh}
	if !c.handleDayWidgetQuery(ctx, q) {
		t.Fatal("query not acknowledged")
	}
	if calls != before+1 {
		t.Fatal("unauthorized request performed edit")
	}
}

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
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
	"go.uber.org/zap"
)

func TestDayTextFallbackAndBoundActions(t *testing.T) {
	ctx := context.Background()
	store, e := sqlite.New(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	if e = store.RunMigrations(ctx); e != nil {
		t.Fatal(e)
	}
	if e = store.EnsureDaySchema(ctx); e != nil {
		t.Fatal(e)
	}
	var keyboard models.InlineKeyboardMarkup
	var calls []string
	b, e := bot.New("123:fixture", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, &http.Client{Transport: presentationTransport(func(r *http.Request) (*http.Response, error) {
		_ = r.ParseMultipartForm(1 << 20)
		calls = append(calls, r.URL.Path)
		status := 200
		result := `{"message_id":9,"chat":{"id":42,"type":"private"}}`
		if strings.HasSuffix(r.URL.Path, "sendWidget") {
			status = 400
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":false,"error_code":400,"description":"unsupported fixture"}`))}, nil
		}
		if strings.HasSuffix(r.URL.Path, "sendMessage") {
			if strings.Contains(r.FormValue("text"), "4111 1111") {
				t.Fatal("text privacy bypass")
			}
			if r.FormValue("reply_markup") != "" {
				_ = json.Unmarshal([]byte(r.FormValue("reply_markup")), &keyboard)
			}
		} else {
			result = `true`
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":` + result + `}`))}, nil
	})}))
	if e != nil {
		t.Fatal(e)
	}
	c := &Channel{bot: b, instanceID: "test", store: store, userResolver: dayTestResolver{}, logger: zap.NewNop(), sentMsgs: newSentTracker()}
	c.day = &DayController{Store: store, Actions: true, Service: func(string) *dayview.Service { return &dayview.Service{Sources: []dayview.Source{dayTestSource{}}} }}
	m := &models.Message{Text: "/day", Chat: models.Chat{ID: 42, Type: models.ChatTypePrivate}, From: &models.User{ID: 42, LanguageCode: "en"}}
	c.openDay(ctx, m, "alice")
	if len(calls) != 2 || len(keyboard.InlineKeyboard) == 0 {
		t.Fatalf("widget fallback missing: %v", calls)
	}
	handle := ""
	for _, row := range keyboard.InlineKeyboard {
		for _, button := range row {
			if strings.Contains(button.CallbackData, ":refresh:") {
				handle = button.CallbackData
			}
		}
	}
	if handle == "" {
		t.Fatal("text mode not interactive")
	}
	before := len(calls)
	cq := &models.CallbackQuery{ID: "q", From: models.User{ID: 43}, Data: handle, Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: 9, Chat: m.Chat}}}
	if !c.handleDayCallback(ctx, cq) {
		t.Fatal("callback ignored")
	}
	if len(calls) != before+1 {
		t.Fatal("foreign actor triggered work")
	}
	id := strings.Split(handle, ":")[1]
	card, e := store.GetDayCard(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	var data cardData
	if json.Unmarshal([]byte(card.ItemsJSON), &data) != nil || !data.TextMode {
		t.Fatal("text mode not durable")
	}
	if e = c.deliverDayCard(ctx, card); e != nil {
		t.Fatal(e)
	}
	if !strings.HasSuffix(calls[len(calls)-1], "editMessageText") {
		t.Fatal("outbox did not preserve text renderer")
	}
}

func TestInlineCalculatorAndProductionProofGate(t *testing.T) {
	for query, want := range map[string]string{"calc (12 + 3) * 2": "30", "time 2026-12-01T12:00:00+03:00 Europe/Berlin": "10:00"} {
		got, e := inlineCalculator(query)
		if e != nil || !strings.Contains(got, want) {
			t.Fatalf("%q %q %v", query, got, e)
		}
	}
	for _, query := range []string{"calc os.Exit(0)", "calc 1 / 0", "calc \"secret\"", "time invalid UTC"} {
		if _, e := inlineCalculator(query); e == nil {
			t.Fatal("invalid tool input accepted")
		}
	}
	c := &Channel{}
	c.SetInlineWidgetTestMode(true)
	if c.inlineEnabled() {
		t.Fatal("production channel enabled through boolean")
	}
}

func TestPublicInlineAndSharedDoNotReadPrivateContext(t *testing.T) {
	ctx := context.Background()
	store, e := sqlite.New(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	p := &dayview.PublicSnapshot{ID: "public-id", Owner: "alice", Instance: "test", WidgetID: "widget", Text: "Explicit public selection", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	if e = store.SavePublicSnapshot(ctx, p); e != nil {
		t.Fatal(e)
	}
	p.WidgetID = "widget-updated"
	if e = store.SavePublicSnapshot(ctx, p); e != nil {
		t.Fatal(e)
	}
	foreign := *p
	foreign.Owner = "bob"
	foreign.WidgetID = "forged"
	if e = store.SavePublicSnapshot(ctx, &foreign); e == nil {
		t.Fatal("public snapshot ownership overwritten")
	}
	var results, answer string
	b, e := bot.New("123:fixture", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, &http.Client{Transport: presentationTransport(func(r *http.Request) (*http.Response, error) {
		_ = r.ParseMultipartForm(1 << 20)
		if strings.HasSuffix(r.URL.Path, "answerInlineQuery") {
			results = r.FormValue("results")
		}
		if strings.HasSuffix(r.URL.Path, "answerWidgetQuery") {
			answer = r.FormValue("data")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":true}`))}, nil
	})}))
	if e != nil {
		t.Fatal(e)
	}
	c := &Channel{bot: b, instanceID: "test", inlineTest: true, testEnvironment: true, day: &DayController{Store: store}}
	c.answerPublicInline(ctx, &models.InlineQuery{ID: "q", From: &models.User{ID: 99}, Query: "calc 4 * 5"})
	if !strings.Contains(results, "4 * 5 = 20") || strings.Contains(results, "alice") {
		t.Fatal(results)
	}
	c.answerPublicInline(ctx, &models.InlineQuery{ID: "q", From: &models.User{ID: 99}, Query: "share public-id"})
	if !strings.Contains(results, "widget-updated") || strings.Contains(results, "alice") {
		t.Fatal(results)
	}
	q := &models.SharedWidgetQuery{ID: "s", WidgetID: "widget-updated", From: models.User{ID: 99}, Data: "pub:public-id", Type: models.WidgetQueryType{Type: models.WidgetQueryTypeTypeData}}
	c.answerPublicShared(ctx, q)
	var page map[string]string
	if json.Unmarshal([]byte(answer), &page) != nil || page["t"] != p.Text {
		t.Fatal(answer)
	}
	q.Data = "d:private:1:complete:0"
	c.answerPublicShared(ctx, q)
	if strings.Contains(answer, p.Text) {
		t.Fatal("private action accepted")
	}
	p.Withdrawn = true
	if e = store.SavePublicSnapshot(ctx, p); e != nil {
		t.Fatal(e)
	}
	q.Data = "pub:public-id"
	c.answerPublicShared(ctx, q)
	if !strings.Contains(answer, "withdrawn") {
		t.Fatal(answer)
	}
}

func TestPublicationRequiresExactOwnerApproval(t *testing.T) {
	for _, tc := range []struct {
		name           string
		approve, pause bool
	}{{"cancel", false, false}, {"publish", true, false}, {"pause_during_approval", true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			approve := tc.approve
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			store, e := sqlite.New(":memory:")
			if e != nil {
				t.Fatal(e)
			}
			defer store.Close()
			if e = store.RunMigrations(ctx); e != nil {
				t.Fatal(e)
			}
			if e = store.EnsureDaySchema(ctx); e != nil {
				t.Fatal(e)
			}
			keyboardCh := make(chan models.InlineKeyboardMarkup, 1)
			published := make(chan string, 2)
			b, e := bot.New("123:fixture", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, &http.Client{Transport: presentationTransport(func(r *http.Request) (*http.Response, error) {
				_ = r.ParseMultipartForm(1 << 20)
				result := `true`
				if strings.HasSuffix(r.URL.Path, "sendMessage") {
					result = `{"message_id":10,"chat":{"id":42,"type":"private"}}`
					if raw := r.FormValue("reply_markup"); strings.Contains(raw, "cfm:") {
						var kb models.InlineKeyboardMarkup
						_ = json.Unmarshal([]byte(raw), &kb)
						keyboardCh <- kb
					}
					if strings.Contains(r.FormValue("text"), "4111 1111") {
						t.Error("unmasked publication preview")
					}
				}
				if strings.HasSuffix(r.URL.Path, "sendWidget") {
					published <- r.FormValue("public_data")
					result = `{"message_id":11,"chat":{"id":42,"type":"private"},"widget":{"id":"shared-widget","html":"fixture"}}`
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":` + result + `}`))}, nil
			})}))
			if e != nil {
				t.Fatal(e)
			}
			c := &Channel{bot: b, store: store, instanceID: "test", inlineTest: true, testEnvironment: true, confirms: newConfirmState(), sentMsgs: newSentTracker(), logger: zap.NewNop(), day: &DayController{Store: store}}
			card := &dayview.Card{ID: "card", Owner: "alice", Instance: "test", Chat: 42, Revision: 1, Expires: time.Now().Add(time.Hour)}
			if e = store.SaveDayCard(ctx, card); e != nil {
				t.Fatal(e)
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				c.publishSnapshot(ctx, card, cardData{Locale: "en", Conversation: "42"}, "Public selection 4111 1111 1111 1111")
			}()
			var kb models.InlineKeyboardMarkup
			select {
			case kb = <-keyboardCh:
			case <-ctx.Done():
				t.Fatal("confirmation not shown")
			}
			// Wait until the confirmation message is bound, without guessing approval state.
			for {
				c.confirms.mu.Lock()
				n := len(c.confirms.pending)
				c.confirms.mu.Unlock()
				if n > 0 {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("confirmation not registered")
				case <-time.After(time.Millisecond):
				}
			}
			callback := kb.InlineKeyboard[0][0].CallbackData
			foreign := &models.CallbackQuery{ID: "foreign", Data: callback, From: models.User{ID: 99}, Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: 10, Chat: models.Chat{ID: 42}}}}
			c.HandleConfirmCallback(foreign)
			select {
			case <-published:
				t.Fatal("wrong actor published")
			default:
			}
			if tc.pause {
				if e = store.PutTelegramTopicState(ctx, &domain.TelegramTopicState{ConversationID: "42", Status: "paused"}); e != nil {
					t.Fatal(e)
				}
			}
			foreign.ID = "owner"
			foreign.From.ID = 42
			if !approve {
				foreign.Data = kb.InlineKeyboard[0][1].CallbackData
			}
			c.HandleConfirmCallback(foreign)
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("publication did not complete")
			}
			select {
			case raw := <-published:
				if !approve || tc.pause {
					t.Fatal("cancel or paused topic published")
				}
				if strings.Contains(raw, "4111 1111") || strings.Contains(raw, "alice") {
					t.Fatal("public payload leaked private data")
				}
			case <-time.After(10 * time.Millisecond):
				if approve && !tc.pause {
					t.Fatal("approved snapshot not sent")
				}
			}
		})
	}
}

type personalDayResolver struct{}

func (personalDayResolver) ResolveUser(_ context.Context, _, id, _, _ string) (string, error) {
	return "owner-" + id, nil
}

type personalDaySource struct{}

func (personalDaySource) Name() string { return "todoist" }
func (personalDaySource) List(_ context.Context, owner string, _ time.Time, _ *time.Location) ([]dayview.Item, error) {
	return []dayview.Item{{Source: "todoist", Title: "Personal data for " + owner}}, nil
}
func (personalDaySource) Change(context.Context, string, dayview.Item, string, time.Time) error {
	return dayview.ErrUnavailable
}
func TestGroupMyDayButtonDeliversOnlyPersonalCallback(t *testing.T) {
	var texts, params, chats []string
	b, e := bot.New("123:fixture", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, &http.Client{Transport: presentationTransport(func(r *http.Request) (*http.Response, error) {
		_ = r.ParseMultipartForm(1 << 20)
		result := `true`
		if strings.HasSuffix(r.URL.Path, "sendMessage") {
			texts = append(texts, r.FormValue("text"))
			params = append(params, r.FormValue("ephemeral_message_parameters"))
			chats = append(chats, r.FormValue("chat_id"))
			result = `{"message_id":9,"chat":{"id":42,"type":"private"}}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":` + result + `}`))}, nil
	})}))
	if e != nil {
		t.Fatal(e)
	}
	c := &Channel{bot: b, instanceID: "test", userResolver: personalDayResolver{}, day: &DayController{Service: func(string) *dayview.Service { return &dayview.Service{Sources: []dayview.Source{personalDaySource{}}} }}}
	m := &models.Message{Text: "/day", Chat: models.Chat{ID: -5, Type: models.ChatTypeGroup}, From: &models.User{ID: 42, LanguageCode: "en"}}
	if !c.handleDayCommand(context.Background(), m) {
		t.Fatal("group command ignored")
	}
	if len(texts) != 1 || strings.Contains(texts[0], "owner-") {
		t.Fatal("personal data in group message")
	}
	cq := &models.CallbackQuery{ID: "private-callback", Data: "myday:personal", From: *m.From, Message: models.MaybeInaccessibleMessage{Message: m}}
	c.handleDayPersonal(context.Background(), cq)
	if len(texts) != 2 || !strings.Contains(texts[1], "owner-42") || strings.Contains(texts[1], "owner-43") {
		t.Fatal("personal projection missing or foreign")
	}
	if chats[1] != "" || !strings.Contains(params[1], "private-callback") {
		t.Fatal("result sent to ordinary/group message")
	}
}

func TestPublicWidgetPagesByteBudgetAndWithdrawal(t *testing.T) {
	if e := validateWidgetHTML(publicWidgetHTML); e != nil {
		t.Fatal(e)
	}
	p := &dayview.PublicSnapshot{ID: strings.Repeat("a", 32), Text: strings.Repeat("😀<\x00", 500), ExpiresAt: time.Now().Add(time.Hour)}
	for _, index := range []int{-1, 0, 1, 3, 14, 999} {
		raw := publicWidgetPage(p, index)
		if len(raw) > 1024 {
			t.Fatalf("public state over budget: %d", len(raw))
		}
		var state map[string]string
		if json.Unmarshal([]byte(raw), &state) != nil {
			t.Fatal("invalid json")
		}
		for _, key := range []string{"p", "n", "q"} {
			if len(state[key]) > 64 {
				t.Fatal("query exceeds Telegram budget")
			}
		}
	}
	p.Withdrawn = true
	raw := publicWidgetPage(p, 0)
	if !strings.Contains(raw, "withdrawn") || strings.Contains(raw, "😀") {
		t.Fatal("withdrawn content shown")
	}
	p.Withdrawn = false
	p.ExpiresAt = time.Now().Add(-time.Second)
	if !strings.Contains(publicWidgetPage(p, 0), "withdrawn") {
		t.Fatal("expired publication still active")
	}
}

func TestPausedTopicOldDayCardCannotReadWriteDeliverOrPublish(t *testing.T) {
	ctx := context.Background()
	store, e := sqlite.New(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	if e = store.RunMigrations(ctx); e != nil {
		t.Fatal(e)
	}
	if e = store.EnsureDaySchema(ctx); e != nil {
		t.Fatal(e)
	}
	calls := 0
	b, e := bot.New("123:fixture", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, &http.Client{Transport: presentationTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":true}`))}, nil
	})}))
	if e != nil {
		t.Fatal(e)
	}
	c := &Channel{bot: b, instanceID: "test", store: store, userResolver: dayTestResolver{}, inlineTest: true, testEnvironment: true, logger: zap.NewNop(), sentMsgs: newSentTracker(), day: &DayController{Store: store, Actions: true, Service: func(string) *dayview.Service { t.Fatal("paused route accessed providers"); return nil }}}
	m := &models.Message{Chat: models.Chat{ID: 42, Type: models.ChatTypePrivate}, MessageThreadID: 77, From: &models.User{ID: 42}}
	key, e := c.conversationFor(ctx, m)
	if e != nil {
		t.Fatal(e)
	}
	if e = c.persistRoute(ctx, key, m, "alice"); e != nil {
		t.Fatal(e)
	}
	if e = store.PutTelegramTopicState(ctx, &domain.TelegramTopicState{ConversationID: key, Status: "paused"}); e != nil {
		t.Fatal(e)
	}
	data := cardData{Conversation: key, Locale: "en", Zone: "UTC", TextMode: true}
	raw, _ := json.Marshal(data)
	card := &dayview.Card{ID: "paused-card", Owner: "alice", Instance: "test", Chat: 42, Thread: 77, Message: 9, Revision: 1, Expires: time.Now().Add(time.Hour), ItemsJSON: string(raw)}
	if _, e = c.dayOwner(ctx, *m.From, m.Chat.ID, m.MessageThreadID); e == nil {
		t.Fatal("paused day owner accepted")
	}
	if e = c.deliverDayCard(ctx, card); e == nil {
		t.Fatal("paused card delivered")
	}
	c.runDayAction(ctx, card, "refresh", 0)
	c.runDayAction(ctx, card, "details", 0)
	c.runDayAction(ctx, card, "complete", 0)
	c.publishSnapshot(ctx, card, data, "public result")
	if calls != 0 {
		t.Fatalf("paused card called Telegram %d times", calls)
	}
}

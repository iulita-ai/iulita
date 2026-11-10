package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
	"go.uber.org/zap"
)

type deletionFixture struct {
	c                               *Channel
	s                               *sqlite.Store
	key, other                      string
	mode                            string
	getMeID                         int64
	topics                          bool
	probes, deletes, sends, notices int
	onDelete                        func()
	onSend                          func(*http.Request)
	jobID                           int64
}

func newDeletionFixture(t *testing.T) *deletionFixture {
	t.Helper()
	s, e := sqlite.New(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	if e = s.RunMigrations(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.EnsureTelegramTopics(ctx); e != nil {
		t.Fatal(e)
	}
	f := &deletionFixture{s: s, mode: "missing", getMeID: 123, topics: true}
	b, e := bot.New("123:fixture", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, &http.Client{Transport: presentationTransport(func(r *http.Request) (*http.Response, error) {
		_ = r.ParseMultipartForm(1 << 20)
		result := `true`
		description := ""
		code := 200
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			result = fmt.Sprintf(`{"id":%d,"is_bot":true,"first_name":"fixture","has_topics_enabled":%t}`, f.getMeID, f.topics)
		case strings.HasSuffix(r.URL.Path, "/editForumTopic"):
			f.probes++
			if r.FormValue("icon_custom_emoji_id") != "-1" || r.FormValue("name") != "" {
				t.Error("probe can modify a real title/icon")
			}
			if r.FormValue("message_thread_id") == "78" {
				description = "DOCUMENT_INVALID"
				break
			}
			switch f.mode {
			case "missing":
				description = "TOPIC_ID_INVALID"
			case "existing":
				description = "DOCUMENT_INVALID"
			case "other400":
				description = "message to edit not found"
			case "send_missing":
				description = "message thread not found" // not sufficient from this negative probe
			case "forbidden":
				description = "bot was blocked by the user"
				code = 403
			case "flood":
				description = "Too Many Requests"
				code = 429
			case "network":
				return nil, context.DeadlineExceeded
			case "success":
			}
		case strings.HasSuffix(r.URL.Path, "/deleteForumTopic"):
			f.deletes++
			if f.onDelete != nil {
				f.onDelete()
			}
			if f.mode == "delete_unknown" {
				description = "internal error"
				code = 500
			}
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			if f.onSend != nil {
				f.onSend(r)
			}
			if r.FormValue("message_thread_id") != "" && r.FormValue("message_thread_id") != "0" {
				f.sends++
				if f.mode == "missing" {
					description = "message thread not found"
				}
			}
			if strings.Contains(r.FormValue("text"), "Контекст очищен:") {
				f.notices++
			}
			result = `{"message_id":30,"chat":{"id":42,"type":"private"}}`
		}
		body := `{"ok":true,"result":` + result + `}`
		if description != "" {
			if code == 200 {
				code = 400
			}
			raw, _ := json.Marshal("Bad Request: " + description)
			body = fmt.Sprintf(`{"ok":false,"error_code":%d,"description":%s}`, code, raw)
		}
		return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}))
	if e != nil {
		t.Fatal(e)
	}
	f.c = &Channel{bot: b, botID: 123, topicsEnabled: true, instanceID: "a", username: "FixtureBot", store: s, allowedIDs: map[int64]struct{}{42: {}}, sentMsgs: newSentTracker(), statusMsgs: newStatusState(), prompts: newPromptState(), confirms: newConfirmState(), remembers: newRememberState(), logger: zap.NewNop()}
	for _, thread := range []int{77, 78} {
		m := &models.Message{Chat: models.Chat{ID: 42, Type: models.ChatTypePrivate}, From: &models.User{ID: 42}, MessageThreadID: thread}
		key := f.c.routeKey(42, thread)
		if e = f.c.persistRoute(ctx, key, m, "alice"); e != nil {
			t.Fatal(e)
		}
		if e = s.PutTelegramTopicState(ctx, &domain.TelegramTopicState{ConversationID: key, Name: "Private topic", Status: "open"}); e != nil {
			t.Fatal(e)
		}
		if e = s.SaveMessage(ctx, &domain.ChatMessage{ChatID: key, UserID: "alice", Role: domain.RoleUser, Content: "private history marker", CreatedAt: time.Now()}); e != nil {
			t.Fatal(e)
		}
		if e = s.CreateReminder(ctx, &domain.Reminder{ChatID: key, UserID: "alice", Title: "Reminder", DueAt: time.Now().Add(time.Hour), Status: "pending"}); e != nil {
			t.Fatal(e)
		}
		if thread == 77 {
			f.key = key
		} else {
			f.other = key
		}
	}
	j := &domain.AgentJob{UserID: "alice", Name: "Bound job", Prompt: "Read", DeliveryChatID: f.key, Enabled: true}
	if e = s.CreateAgentJob(ctx, j); e != nil {
		t.Fatal(e)
	}
	f.jobID = j.ID
	return f
}

func TestManualDeletionSyncCleansOnlyBoundTopic(t *testing.T) {
	f := newDeletionFixture(t)
	ctx := context.Background()
	f.c.sentMsgs.record(42, 1, "deleted raw text", 77)
	f.c.sentMsgs.record(42, 2, "neighbor raw text", 78)
	f.c.statusMsgs.create(f.key, 42, 20)
	f.c.statusMsgs.create(f.other, 42, 21)
	cancelled := ""
	f.c.SetTopicDeletionFunc(func(_ context.Context, key string) error { cancelled = key; return nil })
	if e := f.c.ReconcileTopicDeletions(ctx); e != nil {
		t.Fatal(e)
	}
	h, e := f.s.GetHistory(ctx, f.key, 0)
	if e != nil || len(h) != 0 {
		t.Fatal("context survived deletion", h, e)
	}
	other, e := f.s.GetHistory(ctx, f.other, 0)
	if e != nil || len(other) != 1 {
		t.Fatal("neighbor context lost", e)
	}
	reminders, e := f.s.ListReminders(ctx, f.key)
	if e != nil || len(reminders) != 0 {
		t.Fatal("bound reminders retained")
	}
	job, e := f.s.GetAgentJob(ctx, f.jobID)
	if e != nil || job.Enabled {
		t.Fatal("bound job enabled")
	}
	if cancelled != f.key || f.notices != 1 {
		t.Fatal("missing scoped cancellation/notice")
	}
	f.c.sentMsgs.mu.Lock()
	retained := append([]sentMsg(nil), f.c.sentMsgs.msgs[42]...)
	f.c.sentMsgs.mu.Unlock()
	if len(retained) != 1 || retained[0].msgID != 2 {
		t.Fatal("cache eviction crossed topic boundary", retained)
	}
	if _, ok := f.c.statusMsgs.get(f.key); ok {
		t.Fatal("status cache retained")
	}
	if _, ok := f.c.statusMsgs.get(f.other); !ok {
		t.Fatal("neighbor status lost")
	}
	// A previously issued HTTP send/format goroutine can return after cleanup.
	// Terminal cache fences must reject its late raw text and bookmark/status.
	f.c.sentMsgs.record(42, 3, "late deleted raw text", 77)
	f.c.statusMsgs.create(f.key, 42, 22).addLine("late status")
	f.c.statusMsgs.setReplyTo(f.key, 22)
	f.c.remembers.store("remember:late", &rememberEntry{chatID: f.key, userID: "alice", content: "late raw context"})
	f.c.sentMsgs.mu.Lock()
	n := len(f.c.sentMsgs.msgs[42])
	f.c.sentMsgs.mu.Unlock()
	if n != 1 {
		t.Fatal("late send resurrected raw cache")
	}
	if _, ok := f.c.statusMsgs.get(f.key); ok {
		t.Fatal("late send resurrected status")
	}
	f.c.remembers.mu.Lock()
	n = len(f.c.remembers.entries)
	f.c.remembers.mu.Unlock()
	if n != 0 {
		t.Fatal("late bookmark resurrected context")
	}
	d, e := f.s.GetTelegramTopicDeletion(ctx, f.key)
	if e != nil || d.Status != "deleted" || d.Messages != 1 || d.Reminders != 1 || d.Jobs != 1 {
		t.Fatal(d, e)
	}
	if e = f.c.ReconcileTopicDeletions(ctx); e != nil {
		t.Fatal(e)
	}
	if f.notices != 1 {
		t.Fatal("duplicate root notice")
	}
}

func TestUnknownProbeNeverDeletesContext(t *testing.T) {
	for _, mode := range []string{"existing", "other400", "send_missing", "forbidden", "flood", "network", "success", "wrong_bot", "mode_disabled"} {
		t.Run(mode, func(t *testing.T) {
			f := newDeletionFixture(t)
			f.mode = mode
			if mode == "wrong_bot" {
				f.getMeID = 999
			}
			if mode == "mode_disabled" {
				f.topics = false
			}
			_ = f.c.ReconcileTopicDeletions(context.Background())
			h, e := f.s.GetHistory(context.Background(), f.key, 0)
			if e != nil || len(h) != 1 {
				t.Fatal("unknown result erased history", e)
			}
			d, e := f.s.GetTelegramTopicDeletion(context.Background(), f.key)
			if e != nil || d != nil {
				t.Fatal("unknown result created deletion", d, e)
			}
			if f.notices != 0 {
				t.Fatal("unknown result claimed deletion")
			}
			if mode == "success" {
				n := f.probes
				_ = f.c.ReconcileTopicDeletions(context.Background())
				if f.probes != n || !f.c.topicProbeDisabled.Load() {
					t.Fatal("contract changed but probe kept issuing writes")
				}
			}
		})
	}
}

func TestApprovedDeletionBlocksBeforeNativeAndRecovers(t *testing.T) {
	f := newDeletionFixture(t)
	ctx := context.Background()
	cancelled := false
	f.c.SetTopicDeletionFunc(func(context.Context, string) error { cancelled = true; return nil })
	op, claimed, e := f.s.ClaimTelegramTopicOperation(ctx, &domain.TelegramTopicOperation{ID: "approved-delete", Digest: "exact-approved-target", OwnerID: "alice", InstanceID: "a", ChatID: 42, ThreadID: 77, Action: "delete"})
	if e != nil || !claimed {
		t.Fatal(e)
	}
	f.onDelete = func() {
		h, historyErr := f.s.GetHistory(ctx, f.key, 0)
		if historyErr != nil || len(h) != 0 || !cancelled {
			t.Error("native deletion started before local fencing/cancel")
		}
	}
	f.mode = "delete_unknown"
	f.c.executeTopicDeletion(ctx, f.key, op)
	d, e := f.s.GetTelegramTopicDeletion(ctx, f.key)
	if e != nil || d.Status != "deleting" || f.notices != 0 {
		t.Fatal("uncertain native delete reported success", d, e)
	}
	if _, e = f.c.resolveTarget(ctx, f.key); !errors.Is(e, domain.ErrConversationDeleted) {
		t.Fatal("deleting route still usable", e)
	}
	f.mode = "existing"
	if e = f.c.ReconcileTopicDeletions(ctx); e != nil {
		t.Fatal(e)
	}
	d, e = f.s.GetTelegramTopicDeletion(ctx, f.key)
	if e != nil || d.Status != "deleted" || f.deletes != 2 || f.notices != 1 {
		t.Fatal("pending deletion not recovered", d, e, f.deletes, f.notices)
	}
}

func TestUnapprovedDeletionDoesNothing(t *testing.T) {
	f := newDeletionFixture(t)
	f.c.executeTopicDeletion(context.Background(), f.key, &domain.TelegramTopicOperation{ID: "not-approved", ChatID: 42, ThreadID: 77, Action: "delete"})
	h, e := f.s.GetHistory(context.Background(), f.key, 0)
	if e != nil || len(h) != 1 || f.deletes != 0 {
		t.Fatal("unapproved deletion changed state")
	}
}

func TestRealSendMissingTopicSynchronizesWithoutPlainRetry(t *testing.T) {
	f := newDeletionFixture(t)
	e := f.c.SendMessage(context.Background(), f.key, "result")
	if !errors.Is(e, domain.ErrConversationDeleted) || f.sends != 1 || f.notices != 1 {
		t.Fatal(e, f.sends, f.notices)
	}
	h, e := f.s.GetHistory(context.Background(), f.key, 0)
	if e != nil || len(h) != 0 {
		t.Fatal("send missing did not clean context")
	}
}

func TestReplacementBotCannotDeletePreviousBotContext(t *testing.T) {
	f := newDeletionFixture(t)
	// Simulate a restart with another valid token under the same instance ID:
	// getMe and the runtime agree, but persisted route provenance differs.
	f.c.botID = 999
	f.getMeID = 999
	if e := f.c.ReconcileTopicDeletions(context.Background()); e != nil {
		t.Fatal(e)
	}
	h, e := f.s.GetHistory(context.Background(), f.key, 0)
	if e != nil || len(h) != 1 || f.probes != 0 || f.deletes != 0 || f.notices != 0 {
		t.Fatal("replacement bot erased or probed old context", e)
	}
	if _, e = f.c.resolveTarget(context.Background(), f.key); e == nil {
		t.Fatal("replacement bot accepted old delivery route")
	}
}

func TestLegacyUnprovenBotRouteNeverAutomaticallyCleaned(t *testing.T) {
	f := newDeletionFixture(t)
	ctx := context.Background()
	key := "tgconv:legacy-unproven"
	if e := f.s.PutTelegramRoute(ctx, &domain.TelegramConversationRoute{ConversationID: key, InstanceID: "a", OwnerID: "alice", ChatID: 42, ThreadID: 79, Private: true}); e != nil {
		t.Fatal(e)
	}
	if e := f.s.SaveMessage(ctx, &domain.ChatMessage{ChatID: key, UserID: "alice", Role: domain.RoleUser, Content: "legacy context", CreatedAt: time.Now()}); e != nil {
		t.Fatal(e)
	}
	if e := f.c.ReconcileTopicDeletions(ctx); e != nil {
		t.Fatal(e)
	}
	h, e := f.s.GetHistory(ctx, key, 0)
	if e != nil || len(h) != 1 {
		t.Fatal("unknown bot route erased", e)
	}
	r, e := f.s.GetTelegramRoute(ctx, key)
	if e != nil || r.BotID != 0 {
		t.Fatal("unknown provenance silently rebound")
	}
}

func TestLegacyBindUsesCapturedExplicitConfirmation(t *testing.T) {
	for _, tc := range []struct{ approve, root bool }{{true, false}, {false, false}, {true, true}} {
		approve := tc.approve
		t.Run(fmt.Sprintf("approve=%t/root=%t", approve, tc.root), func(t *testing.T) {
			f := newDeletionFixture(t)
			f.mode = "existing"
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			key := f.c.routeKey(42, 79)
			m := &models.Message{ID: 500, Chat: models.Chat{ID: 42, Type: models.ChatTypePrivate}, From: &models.User{ID: 42}, MessageThreadID: 79}
			if e := f.s.PutTelegramRoute(ctx, &domain.TelegramConversationRoute{ConversationID: key, InstanceID: "a", OwnerID: "alice", ChatID: 42, ThreadID: 79, Private: true}); e != nil {
				t.Fatal(e)
			}
			if e := f.s.PutTelegramTopicState(ctx, &domain.TelegramTopicState{ConversationID: key, Name: "Legacy topic", Status: "open"}); e != nil {
				t.Fatal(e)
			}
			if e := f.s.SaveMessage(ctx, &domain.ChatMessage{ChatID: key, UserID: "alice", Role: domain.RoleUser, Content: "legacy context", CreatedAt: time.Now()}); e != nil {
				t.Fatal(e)
			}
			confirmation := make(chan models.InlineKeyboardMarkup, 1)
			f.onSend = func(r *http.Request) {
				if strings.Contains(r.FormValue("text"), "Confirmation required") {
					var kb models.InlineKeyboardMarkup
					_ = json.Unmarshal([]byte(r.FormValue("reply_markup")), &kb)
					confirmation <- kb
				}
			}
			done := make(chan struct{})
			if tc.root {
				m.MessageThreadID = 0
				m.Text = "/topic bind 79"
				if !f.c.handleTopicLifecycle(ctx, m, "42", "alice") {
					t.Fatal("root bind command not handled")
				}
				go func() { f.c.wg.Wait(); close(done) }()
			} else {
				go func() { defer close(done); f.c.runTopicLifecycle(ctx, m, key, "alice", "bind", "") }()
			}
			var kb models.InlineKeyboardMarkup
			select {
			case kb = <-confirmation:
			case <-ctx.Done():
				t.Fatal("binding confirmation absent")
			}
			for {
				f.c.confirms.mu.Lock()
				ready := len(f.c.confirms.pending) > 0
				f.c.confirms.mu.Unlock()
				if ready {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("binding not registered")
				case <-time.After(time.Millisecond):
				}
			}
			index := 0
			if !approve {
				index = 1
			}
			query := &models.CallbackQuery{ID: "bind", Data: kb.InlineKeyboard[0][index].CallbackData, From: models.User{ID: 43}, Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: 30, Chat: models.Chat{ID: 42}, MessageThreadID: m.MessageThreadID}}}
			f.c.HandleConfirmCallback(query)
			f.c.confirms.mu.Lock()
			pending := len(f.c.confirms.pending)
			f.c.confirms.mu.Unlock()
			if pending != 1 {
				t.Fatal("foreign actor consumed bind approval")
			}
			query.From.ID = 42
			f.c.HandleConfirmCallback(query)
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("binding did not finish")
			}
			r, e := f.s.GetTelegramRoute(ctx, key)
			if e != nil {
				t.Fatal(e)
			}
			want := int64(0)
			if approve {
				want = 123
			}
			if r.BotID != want {
				t.Fatalf("boundBot=%d want=%d", r.BotID, want)
			}
			h, e := f.s.GetHistory(ctx, key, 0)
			if e != nil || len(h) != 1 || f.probes != 0 || f.deletes != 0 {
				t.Fatal("binding changed history or native topics", e)
			}
		})
	}
}

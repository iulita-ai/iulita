package telegrambusiness

import (
	"context"
	"errors"
	"testing"
	"time"
)

func autoFixture(t *testing.T) *fixture {
	t.Helper()
	f := setup(t, true)
	f.service.options.Budget = &testBudget{}
	f.service.generator = generatorFunc(func(context.Context, Scope, []Message) (string, error) { return "Hello!", nil })
	if e := f.repo.Update(context.Background(), f.scope.Key, func(st *State) error {
		r := AutoRules{Classes: []string{"greeting"}, Timezone: "UTC", StartHour: 9, EndHour: 18, MaxRepliesPerDay: 1}
		st.Connection.Policy.Mode = ModeAuto
		st.Connection.Policy.AutoReply = true
		st.Connection.Policy.AutoRules = &r
		other := r
		st.Peers[99].Policy.Mode = ModeAuto
		st.Peers[99].Policy.AutoReply = true
		st.Peers[99].Policy.AutoRules = &other
		st.Peers[99].Messages[0].Text = "Hello"
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	return f
}
func TestAutomaticReplyNeedsClosedClassHoursAndBothRules(t *testing.T) {
	for _, kind := range []string{"nil_connection", "nil_peer", "quiet", "unmatched", "attachment", "command", "bot", "intersection"} {
		t.Run(kind, func(t *testing.T) {
			f := autoFixture(t)
			if e := f.repo.Update(context.Background(), f.scope.Key, func(st *State) error {
				p := st.Peers[99]
				switch kind {
				case "nil_connection":
					st.Connection.Policy.AutoRules = nil
				case "nil_peer":
					p.Policy.AutoRules = nil
				case "quiet":
					p.Policy.AutoRules.StartHour = 13
				case "unmatched":
					p.Messages[0].Text = "Please write an unrestricted essay"
				case "attachment":
					p.Messages[0].Media = []Media{{Kind: "photo", FileID: "file"}}
				case "command":
					p.Messages[0].Text = "/hello"
				case "bot":
					p.Messages[0].SenderIsBot = true
				case "intersection":
					p.Policy.AutoRules.Classes = []string{"acknowledgement"}
				}
				return nil
			}); e != nil {
				t.Fatal(e)
			}
			if checkErr := f.service.Process(context.Background(), f.scope); checkErr != nil {
				t.Fatal(checkErr)
			}
			if f.api.calls.Load() != 0 {
				t.Fatal("unmatched automatic reply escaped")
			}
		})
	}
}
func TestAutomaticUnknownCountsAgainstReplyCapNoRetry(t *testing.T) {
	f := autoFixture(t)
	f.api.execute = func(context.Context, Request) (Receipt, error) { return Receipt{}, errors.New("uncertain") }
	if e := f.service.Process(context.Background(), f.scope); !errors.Is(e, ErrUnknownOutcome) {
		t.Fatal(e)
	}
	if checkErr := f.service.Handle(context.Background(), Event{Key: f.scope.Key, OwnerID: "alice", PeerID: 99, Generation: "generation", UpdateID: 3, Kind: "message", Message: &Message{ID: 11, SenderID: 99, Date: f.now, Revision: 3, Text: "Hi"}}); checkErr != nil {
		t.Fatal(checkErr)
	}
	if checkErr := f.service.Process(context.Background(), f.scope); checkErr != nil {
		t.Fatal(checkErr)
	}
	if f.api.calls.Load() != 1 {
		t.Fatal("unknown reply retried or cap refunded")
	}
}
func TestAutomaticExecuteRechecksQuietHours(t *testing.T) {
	f := autoFixture(t)
	a := approve(t, f, prepare(t, f, "Hello!"))
	if e := f.repo.Update(context.Background(), f.scope.Key, func(st *State) error {
		current := st.Peers[99].Actions[a.ID]
		current.Automatic = true
		current.Digest = digestAction(*current)
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	f.service.options.Now = func() time.Time { return f.now.Add(6 * time.Hour) }
	if _, e := f.service.Execute(context.Background(), f.scope, a.ID); e == nil {
		t.Fatal("queued automatic action crossed quiet hours")
	}
	if f.api.calls.Load() != 0 {
		t.Fatal("quiet-hours request sent")
	}
}
func TestAutoRuleValidationAndOvernightTimezone(t *testing.T) {
	r := &AutoRules{Classes: []string{"greeting"}, Timezone: "Europe/Moscow", StartHour: 22, EndHour: 6, MaxRepliesPerDay: 1}
	if !autoRuleAllows(r, "greeting", time.Date(2026, 11, 26, 20, 0, 0, 0, time.UTC)) || autoRuleAllows(r, "greeting", time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)) {
		t.Fatal("wrong timezone overnight boundary")
	}
	r.Classes = []string{"anything"}
	if validateAutoRules(r) == nil {
		t.Fatal("open class accepted")
	}
}

func TestAutomaticDayCapUsesDispatchAdmissionAndSurvivesPurge(t *testing.T) {
	f := autoFixture(t)
	if e := f.repo.Update(context.Background(), f.scope.Key, func(st *State) error {
		initPeer(st, 99).Actions["old"] = &Action{ID: "old", Scope: f.scope, Method: SendText, Automatic: true, State: "unknown", CreatedAt: f.now.Add(-24 * time.Hour), DispatchAt: f.now}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if checkErr := f.service.Process(context.Background(), f.scope); checkErr != nil {
		t.Fatal(checkErr)
	}
	if f.api.calls.Load() != 0 {
		t.Fatal("same-day dispatch charged to previous creation day")
	}
	if checkErr := f.service.Purge(context.Background(), f.scope); checkErr != nil {
		t.Fatal(checkErr)
	}
	st, _ := f.repo.Get(context.Background(), f.scope.Key)
	a := st.Peers[99].Actions["old"]
	if a.State != "unknown" || !a.Automatic || !a.DispatchAt.Equal(f.now) {
		t.Fatal("purge refunded unknown automatic reply")
	}
}

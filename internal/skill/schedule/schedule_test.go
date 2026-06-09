package schedule

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
)

func newStore(t *testing.T) *sqlite.Store {
	t.Helper()
	store, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("creating store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.RunMigrations(context.Background()); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	return store
}

func userCtx(userID, chatID, role string) context.Context {
	ctx := skill.WithUserID(context.Background(), userID)
	ctx = skill.WithChatID(ctx, chatID)
	ctx = skill.WithUserRole(ctx, role)
	return ctx
}

func call(t *testing.T, s *Skill, ctx context.Context, in input) (string, error) {
	t.Helper()
	raw, _ := json.Marshal(in)
	return s.Execute(ctx, raw)
}

func TestNormalizeSchedule(t *testing.T) {
	tests := []struct {
		name      string
		in        input
		wantCron  string
		wantInt   string
		wantError bool
	}{
		{"cron only", input{CronExpr: "0 9 * * 1-5"}, "0 9 * * 1-5", "24h", false},
		{"cron with tz", input{CronExpr: "0 9 * * 1-5", Timezone: "Europe/Helsinki"}, "CRON_TZ=Europe/Helsinki 0 9 * * 1-5", "24h", false},
		{"cron utc tz ignored", input{CronExpr: "0 9 * * *", Timezone: "UTC"}, "0 9 * * *", "24h", false},
		{"interval only", input{Interval: "6h"}, "", "6h", false},
		{"neither", input{}, "", "", true},
		{"bad cron", input{CronExpr: "not a cron"}, "", "", true},
		{"bad interval", input{Interval: "banana"}, "", "", true},
		{"interval too short", input{Interval: "10s"}, "", "", true},
		{"bad tz", input{CronExpr: "0 9 * * *", Timezone: "Mars/Phobos"}, "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cron, interval, err := normalizeSchedule(tt.in)
			if tt.wantError {
				if err == nil {
					t.Fatalf("expected error, got cron=%q interval=%q", cron, interval)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cron != tt.wantCron || interval != tt.wantInt {
				t.Errorf("got cron=%q interval=%q, want cron=%q interval=%q", cron, interval, tt.wantCron, tt.wantInt)
			}
		})
	}
}

func TestCreate_RequiresNameAndPrompt(t *testing.T) {
	s := New(newStore(t))
	ctx := userCtx("u1", "chat1", "user")
	if _, err := call(t, s, ctx, input{Action: "create", Prompt: "do it", Interval: "6h"}); err == nil {
		t.Error("expected error for missing name")
	}
	if _, err := call(t, s, ctx, input{Action: "create", Name: "x", Interval: "6h"}); err == nil {
		t.Error("expected error for missing prompt")
	}
}

func TestCreate_ScopedToUserAndChat(t *testing.T) {
	store := newStore(t)
	s := New(store)
	ctx := userCtx("u1", "chat-A", "user")
	if _, err := call(t, s, ctx, input{Action: "create", Name: "morning", Prompt: "summarize", CronExpr: "0 9 * * *", Timezone: "Europe/Helsinki"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	jobs, err := store.ListAgentJobsByUser(context.Background(), "u1")
	if err != nil || len(jobs) != 1 {
		t.Fatalf("expected 1 job for u1, got %d (err=%v)", len(jobs), err)
	}
	j := jobs[0]
	if j.UserID != "u1" {
		t.Errorf("user_id = %q, want u1 (must derive from ctx)", j.UserID)
	}
	if j.DeliveryChatID != "chat-A" {
		t.Errorf("delivery = %q, want chat-A (current chat)", j.DeliveryChatID)
	}
	if !strings.HasPrefix(j.CronExpr, "CRON_TZ=Europe/Helsinki ") {
		t.Errorf("cron = %q, want CRON_TZ prefix", j.CronExpr)
	}
}

func TestCreate_PerUserCap(t *testing.T) {
	s := New(newStore(t))
	s.maxJobsPerUser = 2
	ctx := userCtx("u1", "chat1", "user")
	for i := 0; i < 2; i++ {
		if _, err := call(t, s, ctx, input{Action: "create", Name: "j", Prompt: "p", Interval: "6h"}); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	if _, err := call(t, s, ctx, input{Action: "create", Name: "j", Prompt: "p", Interval: "6h"}); err == nil {
		t.Error("expected cap error on 3rd create")
	}
	// A different user is unaffected by u1's cap.
	if _, err := call(t, s, userCtx("u2", "chat2", "user"), input{Action: "create", Name: "j", Prompt: "p", Interval: "6h"}); err != nil {
		t.Errorf("u2 create should succeed: %v", err)
	}
}

func TestOwnership_NonAdminCannotTouchOthersJob(t *testing.T) {
	store := newStore(t)
	s := New(store)
	// u1 creates a job.
	if _, err := call(t, s, userCtx("u1", "chat1", "user"), input{Action: "create", Name: "secret", Prompt: "p", Interval: "6h"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	jobs, _ := store.ListAgentJobsByUser(context.Background(), "u1")
	id := jobs[0].ID

	// u2 cannot pause or delete it — uniform "not found".
	for _, action := range []string{"pause", "delete", "resume"} {
		if _, err := call(t, s, userCtx("u2", "chat2", "user"), input{Action: action, ID: id}); err == nil {
			t.Errorf("u2 %s on u1's job should fail", action)
		}
	}
	// Job still exists and is enabled.
	jobs, _ = store.ListAgentJobsByUser(context.Background(), "u1")
	if len(jobs) != 1 || !jobs[0].Enabled {
		t.Error("u1's job must be untouched by u2")
	}

	// Admin CAN manage any job.
	if _, err := call(t, s, userCtx("admin1", "chatA", string(domain.RoleAdmin)), input{Action: "pause", ID: id}); err != nil {
		t.Errorf("admin pause should succeed: %v", err)
	}
}

func TestList_UserScopeVsAdmin(t *testing.T) {
	store := newStore(t)
	s := New(store)
	_, _ = call(t, s, userCtx("u1", "c1", "user"), input{Action: "create", Name: "a", Prompt: "p", Interval: "6h"})
	_, _ = call(t, s, userCtx("u2", "c2", "user"), input{Action: "create", Name: "b", Prompt: "p", Interval: "6h"})

	out, err := call(t, s, userCtx("u1", "c1", "user"), input{Action: "list"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, `"a"`) || strings.Contains(out, `"b"`) {
		t.Errorf("u1 should see only its own job, got: %s", out)
	}
	adminOut, _ := call(t, s, userCtx("admin1", "cA", string(domain.RoleAdmin)), input{Action: "list"})
	if !strings.Contains(adminOut, `"a"`) || !strings.Contains(adminOut, `"b"`) {
		t.Errorf("admin should see all jobs, got: %s", adminOut)
	}
}

func TestExecute_RequiresUser(t *testing.T) {
	s := New(newStore(t))
	// No user in context.
	raw, _ := json.Marshal(input{Action: "list"})
	if _, err := s.Execute(context.Background(), raw); err == nil {
		t.Error("expected error when no user in context")
	}
}

func TestOnConfigChanged(t *testing.T) {
	s := New(newStore(t))
	s.OnConfigChanged("skills.schedule.max_jobs_per_user", "5")
	if s.maxJobsPerUser != 5 {
		t.Errorf("maxJobsPerUser = %d, want 5", s.maxJobsPerUser)
	}
	// Bad value ignored.
	s.OnConfigChanged("skills.schedule.max_jobs_per_user", "nope")
	if s.maxJobsPerUser != 5 {
		t.Errorf("bad value should be ignored, got %d", s.maxJobsPerUser)
	}
	// Unrelated key ignored.
	s.OnConfigChanged("skills.other.key", "9")
	if s.maxJobsPerUser != 5 {
		t.Errorf("unrelated key should be ignored, got %d", s.maxJobsPerUser)
	}
}

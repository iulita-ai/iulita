package craft

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/dayview"
)

func TestCraftDayActionsReadbackStaleAndCredentialRecovery(t *testing.T) {
	var mu sync.Mutex
	task := Task{ID: "t1", Markdown: "Task", TaskInfo: TaskInfo{State: "todo", ScheduleDate: "2026-11-09"}}
	writes := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPut {
			var in struct {
				Tasks []struct {
					ID   string   `json:"id"`
					Info TaskInfo `json:"taskInfo"`
				} `json:"tasksToUpdate"`
			}
			if e := json.NewDecoder(r.Body).Decode(&in); e != nil {
				t.Error(e)
			}
			if len(in.Tasks) != 1 || in.Tasks[0].ID != task.ID {
				t.Error("wrong target")
			}
			if in.Tasks[0].Info.State != "" {
				task.TaskInfo.State = in.Tasks[0].Info.State
			}
			if in.Tasks[0].Info.ScheduleDate != "" {
				task.TaskInfo.ScheduleDate = in.Tasks[0].Info.ScheduleDate
			}
			writes++
			json.NewEncoder(w).Encode(map[string]any{"items": []Task{task}})
			return
		}
		scope := r.URL.Query().Get("scope")
		items := []Task{}
		if scope == "inbox" && task.TaskInfo.State == "todo" || scope == "logbook" && task.TaskInfo.State == "done" {
			items = append(items, task)
		}
		json.NewEncoder(w).Encode(map[string]any{"items": items})
	}))
	defer srv.Close()
	client := NewClient(srv.URL, "key", srv.Client())
	client.SetNativeOwner("owner")
	d := DaySource{Client: client}
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	items, e := d.List(ctx, "owner", now, time.UTC)
	if e != nil || len(items) != 1 || !items[0].Complete || !items[0].Reschedule {
		t.Fatal(items, e)
	}
	original := items[0]
	target := now.AddDate(0, 0, 1)
	if e = d.Change(ctx, "owner", original, "reschedule", target); e != nil {
		t.Fatal(e)
	}
	v, e := d.Verify(ctx, "owner", original, "reschedule", target)
	if e != nil || v != dayview.EffectObserved {
		t.Fatal(v, e)
	}
	if writes != 1 {
		t.Fatal("verify mutated")
	}
	if e = d.Change(ctx, "owner", original, "complete", time.Time{}); e == nil {
		t.Fatal("stale action accepted")
	}
	mu.Lock()
	task.TaskInfo.ScheduleDate = "2026-11-10"
	mu.Unlock()
	items, e = d.List(ctx, "owner", now, time.UTC)
	if e != nil || len(items) != 1 {
		t.Fatal(items, e)
	}
	if e = d.Change(ctx, "owner", items[0], "complete", time.Time{}); e != nil {
		t.Fatal(e)
	}
	v, e = d.Verify(ctx, "owner", items[0], "complete", time.Time{})
	if e != nil || v != dayview.EffectObserved {
		t.Fatal(v, e)
	}
	if writes != 2 {
		t.Fatal("unexpected writes")
	}
	client.UpdateCredentials(srv.URL, "replacement")
	v, e = d.Verify(ctx, "owner", items[0], "complete", time.Time{})
	if e != nil || v != dayview.Revoked {
		t.Fatal(v, e)
	}
}

func TestCraftFutureScheduleDoesNotHideOverdueDeadline(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	task := Task{TaskInfo: TaskInfo{ScheduleDate: "2026-11-12", DeadlineDate: "2026-11-09"}}
	if !craftDueToday(task, now, time.UTC) {
		t.Fatal("overdue deadline hidden by future schedule")
	}
	i := craftDayItem(task, "c")
	if i.Due != "2026-11-12" || i.Deadline != "2026-11-09" {
		t.Fatal("date semantics lost", i)
	}
	task.TaskInfo.DeadlineDate = "2026-11-13"
	if craftDueToday(task, now, time.UTC) {
		t.Fatal("future task leaked into today")
	}
}

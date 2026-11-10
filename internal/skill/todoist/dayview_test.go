package todoist

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/dayview"
	"go.uber.org/zap"
)

func TestDayChangeRejectsRotatedCredential(t *testing.T) {
	writes := 0
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			writes++
			t.Error("write used rotated credentials")
		}
		if r.URL.Path == "/tasks/t1" {
			json.NewEncoder(w).Encode(Task{ID: "t1", Content: "task"})
			return
		}
		json.NewEncoder(w).Encode(paginatedResponse[Task]{Results: []Task{{ID: "t1", Content: "task"}}})
	})
	c.SetNativeOwner("owner")
	source := DaySource{Skill: NewSkill(c, zap.NewNop())}
	ctx := context.Background()
	items, e := source.List(ctx, "owner", time.Now(), time.UTC)
	if e != nil || len(items) != 1 {
		t.Fatalf("list: %v %+v", e, items)
	}
	c.UpdateToken("new-token")
	if e = source.Change(ctx, "owner", items[0], "complete", time.Time{}); !errors.Is(e, dayview.ErrStale) {
		t.Fatalf("expected stale credential, got %v", e)
	}
	if writes != 0 {
		t.Fatal("mutation happened")
	}
	if _, e = source.List(ctx, "other", time.Now(), time.UTC); e == nil {
		t.Fatal("owner isolation bypass")
	}
}

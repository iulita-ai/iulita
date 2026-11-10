package craft

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/iulita-ai/iulita/internal/dayview"
)

// DaySource provides owner-bound Craft tasks for the personal day view.
type DaySource struct {
	Client  *Client
	Enabled func() bool
}

// Name identifies the Craft task source.
func (DaySource) Name() string { return "craft_tasks" }
func (d DaySource) snapshot(owner string) (*Client, string, error) {
	if d.Client == nil || d.Enabled != nil && !d.Enabled() {
		return nil, "", dayview.ErrUnavailable
	}
	c, e := d.Client.nativeSnapshot(owner, "")
	if e != nil {
		return nil, "", dayview.ErrUnavailable
	}
	base, key := c.getCredentials()
	return c, dayview.Revision(base, key, d.Client.nativeRevision()), nil
}
func craftDayItem(t Task, revision string) dayview.Item {
	due := t.TaskInfo.ScheduleDate
	if due == "" {
		due = t.TaskInfo.DeadlineDate
	}
	open := t.TaskInfo.State == "todo" || t.TaskInfo.State == ""
	return dayview.Item{ID: t.ID, Source: "craft_tasks", Title: t.Markdown, Due: due, Deadline: t.TaskInfo.DeadlineDate, DateOnly: true, ConnectionRevision: revision, Revision: dayview.Revision(t.ID, t.Markdown, t.TaskInfo.State, t.TaskInfo.ScheduleDate, t.TaskInfo.DeadlineDate, revision), Complete: open, Reschedule: open}
}

// List reads open tasks that are undated or due today.
func (d DaySource) List(ctx context.Context, owner string, now time.Time, loc *time.Location) ([]dayview.Item, error) {
	c, rev, e := d.snapshot(owner)
	if e != nil {
		return nil, e
	}
	var out []dayview.Item
	seen := map[string]bool{}
	for _, scope := range []string{"active", "inbox", "upcoming"} {
		tasks, e := c.GetTasks(ctx, scope)
		if e != nil {
			if len(out) > 0 {
				return out, dayview.ErrPartial
			}
			return nil, e
		}
		for _, t := range tasks {
			i := craftDayItem(t, rev)
			if seen[t.ID] || !i.Complete || !craftDueToday(t, now, loc) {
				continue
			}
			seen[t.ID] = true
			out = append(out, i)
			if len(out) >= 1000 {
				return out, dayview.ErrPartial
			}
		}
	}
	return out, nil
}
func craftFindTask(ctx context.Context, c *Client, id string) (*Task, error) {
	for _, scope := range []string{"active", "upcoming", "inbox", "logbook"} {
		tasks, e := c.GetTasks(ctx, scope)
		if e != nil {
			return nil, e
		}
		for _, t := range tasks {
			if t.ID == id {
				return &t, nil
			}
		}
	}
	return nil, dayview.ErrStale
}

// Change applies the confirmed change after checking the pinned task revision.
func (d DaySource) Change(ctx context.Context, owner string, i dayview.Item, action string, due time.Time) error {
	c, revision, e := d.snapshot(owner)
	if e != nil {
		return e
	}
	if i.ConnectionRevision != revision {
		return dayview.ErrStale
	}
	t, e := craftFindTask(ctx, c, i.ID)
	if e != nil {
		return e
	}
	fresh := craftDayItem(*t, revision)
	if fresh.Revision != i.Revision || !fresh.Complete {
		return dayview.ErrStale
	}
	info := map[string]string{}
	switch action {
	case "complete":
		info["state"] = "done"
	case "reschedule":
		if due.IsZero() {
			return dayview.ErrStale
		}
		info["scheduleDate"] = due.Format("2006-01-02")
	default:
		return dayview.ErrUnavailable
	}
	// Craft documents no atomic If-Match contract. Pre-read validates the approved
	// snapshot; the command changes only selected fields and post-read proves effect.
	body := map[string]any{"tasksToUpdate": []map[string]any{{"id": i.ID, "taskInfo": info}}}
	if updateErr := c.doJSON(ctx, http.MethodPut, "/tasks", body, nil); updateErr != nil {
		return updateErr
	}
	after, e := craftFindTask(ctx, c, i.ID)
	if e != nil {
		return e
	}
	if action == "complete" && after.TaskInfo.State != "done" {
		return dayview.ErrStale
	}
	if action == "reschedule" && after.TaskInfo.ScheduleDate != due.Format("2006-01-02") {
		return dayview.ErrStale
	}
	return nil
}

// Verify reads the pinned task to reconcile an uncertain change without retrying it.
func (d DaySource) Verify(ctx context.Context, owner string, i dayview.Item, action string, due time.Time) (dayview.Verification, error) {
	c, rev, e := d.snapshot(owner)
	if e != nil {
		if errors.Is(e, dayview.ErrUnavailable) {
			return dayview.Revoked, nil
		}
		return dayview.Inconclusive, e
	}
	if i.ConnectionRevision == "" || i.ConnectionRevision != rev {
		return dayview.Revoked, nil
	}
	t, e := craftFindTask(ctx, c, i.ID)
	if e != nil {
		return dayview.Inconclusive, e
	}
	switch action {
	case "complete":
		if t.TaskInfo.State == "done" {
			return dayview.EffectObserved, nil
		}
	case "reschedule":
		if t.TaskInfo.ScheduleDate == due.Format("2006-01-02") {
			return dayview.EffectObserved, nil
		}
	default:
		return dayview.Inconclusive, nil
	}
	return dayview.NotObserved, nil
}

// Detail reads the owned task without requiring it to remain on today's page.
func (d DaySource) Detail(ctx context.Context, owner string, i dayview.Item) (dayview.Detail, error) {
	c, rev, e := d.snapshot(owner)
	if e != nil {
		return dayview.Detail{}, e
	}
	if i.ConnectionRevision != rev {
		return dayview.Detail{}, dayview.ErrStale
	}
	t, e := craftFindTask(ctx, c, i.ID)
	if e != nil {
		return dayview.Detail{}, e
	}
	return dayview.Detail{Title: t.Markdown, Status: t.TaskInfo.State, Summary: "Schedule: " + t.TaskInfo.ScheduleDate + "\nDeadline: " + t.TaskInfo.DeadlineDate}, nil
}

func craftDueToday(t Task, now time.Time, loc *time.Location) bool {
	if t.TaskInfo.ScheduleDate == "" && t.TaskInfo.DeadlineDate == "" {
		return true
	}
	return t.TaskInfo.ScheduleDate != "" && dayview.DueToday(t.TaskInfo.ScheduleDate, now, loc) || t.TaskInfo.DeadlineDate != "" && dayview.DueToday(t.TaskInfo.DeadlineDate, now, loc)
}

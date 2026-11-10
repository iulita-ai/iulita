package todoist

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/iulita-ai/iulita/internal/dayview"
)

// DaySource exposes a typed snapshot with the same owner/credential epoch as native execution.
type DaySource struct {
	Skill   *Skill
	Enabled func() bool
}

// Name identifies the Todoist source.
func (DaySource) Name() string { return "todoist" }
func (d DaySource) snapshot(owner string) (*Client, error) {
	c := d.Skill.client
	c.mu.RLock()
	defer c.mu.RUnlock()
	if owner == "" || owner != c.nativeOwner || c.apiToken == "" || (d.Enabled != nil && !d.Enabled()) {
		return nil, dayview.ErrUnavailable
	}
	s := NewClient(c.apiToken, c.httpClient, c.logger)
	s.baseURL = c.baseURL
	s.nativeEpoch = c.nativeEpoch
	return s, nil
}
func taskItem(t Task) dayview.Item {
	due := ""
	if t.Due != nil {
		due = t.Due.Date
		if t.Due.Datetime != "" {
			due = t.Due.Datetime
		}
	}
	return dayview.Item{ID: t.ID, Source: "todoist", Title: t.Content, Due: due, Revision: dayview.Revision(t.ID, t.Content, due), Complete: !t.IsCompleted, DateOnly: false, Reschedule: t.Due == nil || !t.Due.IsRecurring}
}

// List reads bounded pages of active tasks due today or without a due date.
func (d DaySource) List(ctx context.Context, owner string, now time.Time, loc *time.Location) ([]dayview.Item, error) {
	c, e := d.snapshot(owner)
	if e != nil {
		return nil, e
	}
	var out []dayview.Item
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < 100; page++ {
		path := "/tasks?limit=100"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		var resp paginatedResponse[Task]
		if e = c.doJSON(ctx, http.MethodGet, path, nil, &resp); e != nil {
			if len(out) > 0 {
				return out, dayview.ErrPartial
			}
			return nil, e
		}
		for taskIndex := range resp.Results {
			i := taskItem(resp.Results[taskIndex])
			if !i.Complete || !dayview.ItemDueToday(i, now, loc) {
				continue
			}
			i.ConnectionRevision = dayview.Revision(c.apiToken, strconv.FormatUint(c.nativeEpoch, 10))
			i.Revision = dayview.Revision(i.Revision, strconv.FormatUint(c.nativeEpoch, 10))
			out = append(out, i)
			if len(out) >= 1000 {
				return out, dayview.ErrPartial
			}
		}
		if resp.NextCursor == "" {
			return out, nil
		}
		if seen[resp.NextCursor] {
			return out, dayview.ErrPartial
		}
		seen[resp.NextCursor] = true
		cursor = resp.NextCursor
	}
	return out, dayview.ErrPartial
}

// Change executes an approved change against the pinned owner and task revision.
func (d DaySource) Change(ctx context.Context, owner string, item dayview.Item, action string, due time.Time) error {
	c, e := d.snapshot(owner)
	if e != nil {
		return e
	}
	t, e := c.GetTask(ctx, item.ID)
	if e != nil {
		return e
	}
	if item.ConnectionRevision != "" && item.ConnectionRevision != dayview.Revision(c.apiToken, strconv.FormatUint(c.nativeEpoch, 10)) {
		return dayview.ErrStale
	}
	fresh := taskItem(*t)
	fresh.Revision = dayview.Revision(fresh.Revision, strconv.FormatUint(c.nativeEpoch, 10))
	if fresh.Revision != item.Revision || !fresh.Complete {
		return dayview.ErrStale
	}
	switch action {
	case "complete":
		if closeErr := c.CloseTask(ctx, item.ID); closeErr != nil {
			return closeErr
		}
		verified, verifyErr := c.GetTask(ctx, item.ID)
		if verifyErr != nil {
			return verifyErr
		}
		if !verified.IsCompleted {
			return dayview.ErrStale
		}
		return nil
	case "reschedule":
		if !fresh.Reschedule {
			return dayview.ErrUnavailable
		}
		_, e = c.UpdateTask(ctx, item.ID, UpdateTaskParams{DueDatetime: due.Format(time.RFC3339)})
		if e != nil {
			return e
		}
		verified, verifyErr := c.GetTask(ctx, item.ID)
		if verifyErr != nil {
			return verifyErr
		}
		if !todoistDueMatches(verified.Due, due) {
			return dayview.ErrStale
		}
		return nil
	}
	return dayview.ErrUnavailable
}

// Verify reads the pinned task after an uncertain outcome without retrying it.
func (d DaySource) Verify(ctx context.Context, owner string, i dayview.Item, action string, due time.Time) (dayview.Verification, error) {
	c, e := d.snapshot(owner)
	if e != nil {
		if errors.Is(e, dayview.ErrUnavailable) {
			return dayview.Revoked, nil
		}
		return dayview.Inconclusive, e
	}
	if i.ConnectionRevision == "" || i.ConnectionRevision != dayview.Revision(c.apiToken, strconv.FormatUint(c.nativeEpoch, 10)) {
		return dayview.Revoked, nil
	}
	t, e := c.GetTask(ctx, i.ID)
	if e != nil {
		return dayview.Inconclusive, e
	}
	switch action {
	case "complete":
		if t.IsCompleted {
			return dayview.EffectObserved, nil
		}
	case "reschedule":
		if todoistDueMatches(t.Due, due) {
			return dayview.EffectObserved, nil
		}
	default:
		return dayview.Inconclusive, nil
	}
	return dayview.NotObserved, nil
}

// Detail reads owned task details independently of the current day listing.
func (d DaySource) Detail(ctx context.Context, owner string, i dayview.Item) (dayview.Detail, error) {
	c, e := d.snapshot(owner)
	if e != nil {
		return dayview.Detail{}, e
	}
	if i.ConnectionRevision != dayview.Revision(c.apiToken, strconv.FormatUint(c.nativeEpoch, 10)) {
		return dayview.Detail{}, dayview.ErrStale
	}
	t, e := c.GetTask(ctx, i.ID)
	if e != nil {
		return dayview.Detail{}, e
	}
	status := "open"
	if t.IsCompleted {
		status = "completed"
	}
	return dayview.Detail{Title: t.Content, Status: status, Summary: taskItem(*t).Due + "\n" + t.Description}, nil
}

func todoistDueMatches(value *Due, due time.Time) bool {
	if value == nil {
		return false
	}
	if value.Datetime != "" {
		t, e := time.Parse(time.RFC3339, value.Datetime)
		return e == nil && t.Equal(due)
	}
	return false
}

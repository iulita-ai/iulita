package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/iulita-ai/iulita/internal/dayview"
	"github.com/iulita-ai/iulita/internal/domain"
)

// DayJobs never reads raw result/error bodies into Telegram state. Ownership is
// established through the job, not through task payload text supplied by a client.
type DayJobs struct {
	Store *Store
	Chat  string
}

// Name returns the background-job source name.
func (DayJobs) Name() string { return "jobs" }

// List lists jobs and execution summaries bound to the owned conversation.
func (d DayJobs) List(ctx context.Context, owner string, _ time.Time, _ *time.Location) ([]dayview.Item, error) {
	if owner == "" {
		return nil, dayview.ErrUnavailable
	}
	jobs, e := d.Store.ListAgentJobsByUser(ctx, owner)
	if e != nil {
		return nil, e
	}
	var out []dayview.Item
	for index := range jobs {
		j := &jobs[index]
		if j.DeliveryChatID != d.Chat {
			continue
		}
		var tasks []domain.Task
		e = d.Store.db.NewSelect().Model(&tasks).Where("type = ? AND unique_key = ?", "agent.job", fmt.Sprintf("agent.job:%d", j.ID)).Order("id DESC").Limit(1).Scan(ctx)
		if e != nil {
			return nil, e
		}
		if len(tasks) == 0 {
			continue
		}
		t := tasks[0]
		out = append(out, dayview.Item{ID: fmt.Sprint(t.ID), Source: "jobs", Title: j.Name + " · " + string(t.Status), Due: t.ScheduledAt.Format(time.RFC3339), Revision: dayview.Revision(fmt.Sprint(t.ID), string(t.Status))})
	}
	return out, nil
}

// Change rejects mutation of job projections.
func (DayJobs) Change(context.Context, string, dayview.Item, string, time.Time) error {
	return dayview.ErrUnavailable
}

// Detail rechecks job and task ownership before returning a private result.
func (d DayJobs) Detail(ctx context.Context, owner string, item dayview.Item) (dayview.Detail, error) {
	if owner == "" || item.Source != "jobs" {
		return dayview.Detail{}, dayview.ErrUnavailable
	}
	jobs, e := d.Store.ListAgentJobsByUser(ctx, owner)
	if e != nil {
		return dayview.Detail{}, e
	}
	for index := range jobs {
		j := &jobs[index]
		if j.DeliveryChatID != d.Chat {
			continue
		}
		var task domain.Task
		e = d.Store.db.NewSelect().Model(&task).Where("id = ? AND type = ? AND unique_key = ?", item.ID, "agent.job", fmt.Sprintf("agent.job:%d", j.ID)).Scan(ctx)
		if e != nil {
			continue
		}
		if dayview.Revision(fmt.Sprint(task.ID), string(task.Status)) != item.Revision {
			return dayview.Detail{}, dayview.ErrStale
		}
		summary := ""
		if task.Status == domain.TaskStatusFailed {
			summary = "Job failed. The provider error is not displayed."
		}
		result := ""
		if task.Status == domain.TaskStatusDone {
			result = task.Result
		}
		return dayview.Detail{Title: j.Name, Status: string(task.Status), StartedAt: task.StartedAt, FinishedAt: task.FinishedAt, Summary: summary, Result: result}, nil
	}
	return dayview.Detail{}, dayview.ErrUnavailable
}

package google

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/iulita-ai/iulita/internal/dayview"
	"github.com/iulita-ai/iulita/internal/domain"
	"google.golang.org/api/option"
	gtasks "google.golang.org/api/tasks/v1"
)

// DayTasks reads and changes Google Tasks through the owning account connection.
type DayTasks struct {
	Client   *Client
	Enabled  func() bool
	endpoint string
}

// Name identifies the Google Tasks source.
func (DayTasks) Name() string { return "google_tasks" }
func (d DayTasks) service(ctx context.Context, owner, connection string) (*gtasks.Service, *domain.GoogleAccount, error) {
	if owner == "" || d.Client == nil || d.Client.store == nil || (d.Enabled != nil && !d.Enabled()) {
		return nil, nil, dayview.ErrUnavailable
	}
	var a *domain.GoogleAccount
	var e error
	if connection == "" {
		a, e = d.Client.resolveAccount(ctx, owner, "")
	} else {
		id, err := strconv.ParseInt(connection, 10, 64)
		if err != nil {
			return nil, nil, dayview.ErrUnavailable
		}
		a, e = d.Client.store.GetGoogleAccount(ctx, id)
	}
	if e != nil || a == nil || a.UserID != owner {
		return nil, nil, dayview.ErrUnavailable
	}
	ctx = WithAccountPrincipal(ctx, owner)
	ts, e := d.Client.tokenSource(ctx, a)
	if e != nil {
		return nil, nil, e
	}
	opts := apiOptions(ctx, ts)
	if d.endpoint != "" {
		opts = append(opts, option.WithEndpoint(d.endpoint))
	}
	service, e := gtasks.NewService(ctx, opts...)
	return service, a, e
}
func googleAccountRevision(a *domain.GoogleAccount) string {
	return dayview.Revision(strconv.FormatInt(a.ID, 10), a.AccountEmail, a.EncryptedRefreshToken, a.Scopes)
}
func googleConnectionRevision(a *domain.GoogleAccount, item dayview.Item) string {
	return dayview.Revision(item.Revision, item.ListID, strconv.FormatInt(a.ID, 10), a.AccountEmail, a.EncryptedRefreshToken, a.Scopes)
}
func googleItem(t *gtasks.Task) dayview.Item {
	return dayview.Item{ID: t.Id, Source: "google_tasks", Title: t.Title, Due: t.Due, Revision: dayview.Revision(t.Id, t.Etag, t.Updated), Complete: t.Status != "completed" && !t.Deleted, Reschedule: t.Status != "completed" && !t.Deleted, DateOnly: true}
}

// List reads due tasks across a bounded set of the owner's accounts and lists.
func (d DayTasks) List(ctx context.Context, owner string, now time.Time, loc *time.Location) ([]dayview.Item, error) {
	if owner == "" || d.Client == nil || d.Client.store == nil || (d.Enabled != nil && !d.Enabled()) {
		return nil, dayview.ErrUnavailable
	}
	accounts, e := d.Client.store.ListGoogleAccounts(ctx, owner)
	if e != nil {
		return nil, e
	}
	if len(accounts) == 0 {
		return nil, dayview.ErrUnavailable
	}
	var out []dayview.Item
	partial := false
	for ai := range accounts {
		account := &accounts[ai]
		if ai >= 20 {
			partial = true
			break
		}
		s, a, e := d.service(ctx, owner, strconv.FormatInt(account.ID, 10))
		if e != nil {
			partial = true
			continue
		}
		listToken := ""
		for lp := 0; lp < 20; lp++ {
			lists, e := s.Tasklists.List().MaxResults(100).PageToken(listToken).Context(ctx).Do()
			if e != nil {
				partial = true
				break
			}
			for _, list := range lists.Items {
				taskToken := ""
				for tp := 0; tp < 20; tp++ {
					tasks, e := s.Tasks.List(list.Id).ShowCompleted(false).MaxResults(100).PageToken(taskToken).Context(ctx).Do()
					if e != nil {
						partial = true
						break
					}
					for _, t := range tasks.Items {
						if t.Status == "completed" || t.Deleted || !dayview.DueToday(t.Due, now, loc) {
							continue
						}
						i := googleItem(t)
						i.ListID = list.Id
						i.Origin = a.AccountEmail + " / " + list.Title
						i.Connection = strconv.FormatInt(a.ID, 10)
						i.ConnectionRevision = googleAccountRevision(a)
						i.Revision = googleConnectionRevision(a, i)
						out = append(out, i)
						if len(out) >= 1000 {
							return out, dayview.ErrPartial
						}
					}
					if tasks.NextPageToken == "" {
						break
					}
					taskToken = tasks.NextPageToken
					if tp == 19 {
						partial = true
					}
				}
			}
			if lists.NextPageToken == "" {
				break
			}
			listToken = lists.NextPageToken
			if lp == 19 {
				partial = true
			}
		}
	}
	if partial {
		return out, dayview.ErrPartial
	}
	return out, nil
}
func (d DayTasks) read(ctx context.Context, owner string, i dayview.Item) (*gtasks.Service, *domain.GoogleAccount, *gtasks.Task, error) {
	if i.Connection == "" || i.ListID == "" {
		return nil, nil, nil, dayview.ErrStale
	}
	s, a, e := d.service(ctx, owner, i.Connection)
	if e != nil {
		return nil, nil, nil, e
	}
	t, e := s.Tasks.Get(i.ListID, i.ID).Context(ctx).Do()
	return s, a, t, e
}

// Change applies an approved task mutation with the pinned ETag precondition.
func (d DayTasks) Change(ctx context.Context, owner string, i dayview.Item, action string, due time.Time) error {
	s, a, t, e := d.read(ctx, owner, i)
	if e != nil {
		return e
	}
	if i.ConnectionRevision != googleAccountRevision(a) {
		return dayview.ErrStale
	}
	fresh := googleItem(t)
	fresh.ListID = i.ListID
	if googleConnectionRevision(a, fresh) != i.Revision {
		return dayview.ErrStale
	}
	patchBody := &gtasks.Task{}
	switch action {
	case "complete":
		if !fresh.Complete {
			return dayview.ErrStale
		}
		patchBody.Status = "completed"
	case "reschedule":
		if !fresh.Reschedule || due.IsZero() {
			return dayview.ErrStale
		}
		patchBody.Due = due.Format("2006-01-02") + "T00:00:00Z"
	default:
		return dayview.ErrUnavailable
	}
	patch := s.Tasks.Patch(i.ListID, i.ID, patchBody).Context(ctx)
	patch.Header().Set("If-Match", t.Etag)
	if _, e = patch.Do(); e != nil {
		return e
	}
	verified, e := s.Tasks.Get(i.ListID, i.ID).Context(ctx).Do()
	if e != nil {
		return e
	}
	if action == "complete" && verified.Status != "completed" {
		return dayview.ErrStale
	}
	if action == "reschedule" && (len(verified.Due) < 10 || verified.Due[:10] != due.Format("2006-01-02")) {
		return dayview.ErrStale
	}
	return nil
}

// Verify reconciles the pinned task by reading it, without repeating a mutation.
func (d DayTasks) Verify(ctx context.Context, owner string, i dayview.Item, action string, due time.Time) (dayview.Verification, error) {
	_, a, t, e := d.read(ctx, owner, i)
	if e != nil {
		if errors.Is(e, dayview.ErrUnavailable) {
			return dayview.Revoked, nil
		}
		return dayview.Inconclusive, e
	}
	if i.ConnectionRevision == "" || i.ConnectionRevision != googleAccountRevision(a) {
		return dayview.Revoked, nil
	}
	switch action {
	case "complete":
		if t.Status == "completed" {
			return dayview.EffectObserved, nil
		}
	case "reschedule":
		if len(t.Due) >= 10 && t.Due[:10] == due.Format("2006-01-02") {
			return dayview.EffectObserved, nil
		}
	default:
		return dayview.Inconclusive, nil
	}
	return dayview.NotObserved, nil
}

// DayCalendar provides a read-only view of the owner's primary calendar.
type DayCalendar struct {
	Client  *Client
	Enabled func() bool
}

// Name identifies the calendar source.
func (DayCalendar) Name() string { return "calendar" }

// List reads events in the requested local calendar day.
func (d DayCalendar) List(ctx context.Context, owner string, now time.Time, loc *time.Location) ([]dayview.Item, error) {
	if owner == "" || (d.Enabled != nil && !d.Enabled()) || !d.Client.ownedAccountsAvailable(ctx, owner) {
		return nil, dayview.ErrUnavailable
	}
	s, e := d.Client.GetCalendarService(WithAccountPrincipal(ctx, owner), owner, "")
	if e != nil {
		return nil, e
	}
	n := now.In(loc)
	start := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc)
	r, e := s.Events.List("primary").TimeMin(start.Format(time.RFC3339)).TimeMax(start.AddDate(0, 0, 1).Format(time.RFC3339)).SingleEvents(true).OrderBy("startTime").MaxResults(100).Context(ctx).Do()
	if e != nil {
		return nil, e
	}
	var out []dayview.Item
	for _, v := range r.Items {
		due := ""
		if v.Start != nil {
			due = v.Start.DateTime
			if due == "" {
				due = v.Start.Date
			}
		}
		out = append(out, dayview.Item{ID: v.Id, Source: "calendar", Title: v.Summary, Due: due, Revision: v.Etag})
	}
	return out, nil
}

// Change rejects mutations because the calendar day source is read-only.
func (DayCalendar) Change(context.Context, string, dayview.Item, string, time.Time) error {
	return dayview.ErrUnavailable
}

// Detail re-reads the pinned account/list directly; a task need not remain on today's page.
func (d DayTasks) Detail(ctx context.Context, owner string, i dayview.Item) (dayview.Detail, error) {
	_, a, t, e := d.read(ctx, owner, i)
	if e != nil {
		return dayview.Detail{}, e
	}
	if i.ConnectionRevision != googleAccountRevision(a) {
		return dayview.Detail{}, dayview.ErrStale
	}
	return dayview.Detail{Title: t.Title, Status: t.Status, Summary: a.AccountEmail + " / " + i.ListID + "\n" + t.Due + "\n" + t.Notes}, nil
}

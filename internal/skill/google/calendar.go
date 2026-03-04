package google

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	googlecalendar "google.golang.org/api/calendar/v3"

	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/storage"
)

// CalendarSkill views and searches Google Calendar events.
type CalendarSkill struct {
	client *Client
	store  storage.Repository
}

func NewCalendar(client *Client, store storage.Repository) *CalendarSkill {
	return &CalendarSkill{client: client, store: store}
}

func (s *CalendarSkill) Name() string { return "google_calendar" }

func (s *CalendarSkill) Description() string {
	return "View Google Calendar events: today, tomorrow, this week, search by title, or get event details. Shows time, title, location, attendees, and meeting links."
}

func (s *CalendarSkill) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["today", "tomorrow", "week", "search", "event"],
				"description": "Action: today, tomorrow, week (next 7 days), search (by title), event (details by ID)"
			},
			"query": {
				"type": "string",
				"description": "Search query for 'search' action"
			},
			"event_id": {
				"type": "string",
				"description": "Event ID for 'event' action"
			},
			"calendar_id": {
				"type": "string",
				"description": "Calendar ID (default: primary)"
			},
			"account": {
				"type": "string",
				"description": "Google account alias or email"
			}
		},
		"required": ["action"]
	}`)
}

func (s *CalendarSkill) RequiredCapabilities() []string { return []string{"google"} }

type calendarInput struct {
	Action     string `json:"action"`
	Query      string `json:"query"`
	EventID    string `json:"event_id"`
	CalendarID string `json:"calendar_id"`
	Account    string `json:"account"`
}

func (s *CalendarSkill) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in calendarInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	userID := skill.UserIDFrom(ctx)
	if userID == "" {
		return "", fmt.Errorf("user not identified")
	}

	if !s.client.HasAccounts(ctx, userID) {
		return "No Google account connected. Please connect one in Settings.", nil
	}

	srv, err := s.client.GetCalendarService(ctx, userID, in.Account)
	if err != nil {
		return "", fmt.Errorf("creating Calendar service: %w", err)
	}

	calID := in.CalendarID
	if calID == "" {
		calID = "primary"
	}

	tz := GetUserTimezone(ctx, s.store, userID)
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}

	switch in.Action {
	case "today":
		start, end := dayRange(time.Now().In(loc), 0)
		return s.listRange(srv, calID, start, end, loc)
	case "tomorrow":
		start, end := dayRange(time.Now().In(loc), 1)
		return s.listRange(srv, calID, start, end, loc)
	case "week":
		return s.listWeek(srv, calID, loc)
	case "search":
		if in.Query == "" {
			return "", fmt.Errorf("query is required for search action")
		}
		return s.search(srv, calID, in.Query, loc)
	case "event":
		if in.EventID == "" {
			return "", fmt.Errorf("event_id is required for event action")
		}
		return s.eventDetail(srv, calID, in.EventID, loc)
	default:
		return "", fmt.Errorf("unknown action %q", in.Action)
	}
}

func dayRange(now time.Time, offset int) (time.Time, time.Time) {
	day := now.AddDate(0, 0, offset)
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location())
	end := start.Add(24 * time.Hour)
	return start, end
}

func (s *CalendarSkill) listRange(srv *googlecalendar.Service, calID string, start, end time.Time, loc *time.Location) (string, error) {
	events, err := srv.Events.List(calID).
		TimeMin(start.Format(time.RFC3339)).
		TimeMax(end.Format(time.RFC3339)).
		SingleEvents(true).
		OrderBy("startTime").
		MaxResults(50).
		Do()
	if err != nil {
		return "", fmt.Errorf("listing events: %w", err)
	}

	dayLabel := start.Format("Monday, January 2")
	if len(events.Items) == 0 {
		return fmt.Sprintf("No events on %s.", dayLabel), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "**%s** (%d events):\n\n", dayLabel, len(events.Items))
	for i, e := range events.Items {
		b.WriteString(formatEvent(e, i+1, loc))
	}
	return b.String(), nil
}

func (s *CalendarSkill) listWeek(srv *googlecalendar.Service, calID string, loc *time.Location) (string, error) {
	now := time.Now().In(loc)
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	end := start.AddDate(0, 0, 7)

	events, err := srv.Events.List(calID).
		TimeMin(start.Format(time.RFC3339)).
		TimeMax(end.Format(time.RFC3339)).
		SingleEvents(true).
		OrderBy("startTime").
		MaxResults(100).
		Do()
	if err != nil {
		return "", fmt.Errorf("listing week events: %w", err)
	}

	if len(events.Items) == 0 {
		return "No events this week.", nil
	}

	// Group by day.
	grouped := make(map[string][]*googlecalendar.Event)
	order := make([]string, 0)
	for _, e := range events.Items {
		t := parseEventStart(e, loc)
		day := t.Format("Monday, January 2")
		if _, ok := grouped[day]; !ok {
			order = append(order, day)
		}
		grouped[day] = append(grouped[day], e)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "**Week overview** (%d events):\n\n", len(events.Items))
	for _, day := range order {
		fmt.Fprintf(&b, "### %s\n", day)
		for i, e := range grouped[day] {
			b.WriteString(formatEvent(e, i+1, loc))
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

func (s *CalendarSkill) search(srv *googlecalendar.Service, calID, query string, loc *time.Location) (string, error) {
	events, err := srv.Events.List(calID).
		Q(query).
		SingleEvents(true).
		OrderBy("startTime").
		MaxResults(20).
		Do()
	if err != nil {
		return "", fmt.Errorf("searching events: %w", err)
	}

	if len(events.Items) == 0 {
		return fmt.Sprintf("No events matching %q.", query), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Found %d event(s) matching %q:\n\n", len(events.Items), query)
	for i, e := range events.Items {
		b.WriteString(formatEvent(e, i+1, loc))
	}
	return b.String(), nil
}

func (s *CalendarSkill) eventDetail(srv *googlecalendar.Service, calID, eventID string, loc *time.Location) (string, error) {
	e, err := srv.Events.Get(calID, eventID).Do()
	if err != nil {
		return "", fmt.Errorf("getting event: %w", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "**%s**\n", e.Summary)
	fmt.Fprintf(&b, "Time: %s\n", formatEventTime(e, loc))
	if e.Location != "" {
		fmt.Fprintf(&b, "Location: %s\n", e.Location)
	}
	if meetLink := getMeetLink(e); meetLink != "" {
		fmt.Fprintf(&b, "Meeting: %s\n", meetLink)
	}
	if e.Description != "" {
		desc := e.Description
		if len(desc) > 2000 {
			desc = desc[:2000] + "..."
		}
		fmt.Fprintf(&b, "\nDescription:\n%s\n", desc)
	}
	if len(e.Attendees) > 0 {
		fmt.Fprintf(&b, "\nAttendees (%d):\n", len(e.Attendees))
		for _, a := range e.Attendees {
			name := a.DisplayName
			if name == "" {
				name = a.Email
			}
			status := a.ResponseStatus
			if a.Organizer {
				status += " (organizer)"
			}
			fmt.Fprintf(&b, "- %s — %s\n", name, status)
		}
	}
	return b.String(), nil
}

func formatEvent(e *googlecalendar.Event, num int, loc *time.Location) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d. **%s**\n", num, e.Summary)
	fmt.Fprintf(&b, "   Time: %s\n", formatEventTime(e, loc))
	if e.Location != "" {
		fmt.Fprintf(&b, "   Location: %s\n", e.Location)
	}
	if meetLink := getMeetLink(e); meetLink != "" {
		fmt.Fprintf(&b, "   Meet: %s\n", meetLink)
	}
	if len(e.Attendees) > 0 {
		names := make([]string, 0, len(e.Attendees))
		for _, a := range e.Attendees {
			n := a.DisplayName
			if n == "" {
				n = a.Email
			}
			names = append(names, n)
		}
		if len(names) > 5 {
			names = append(names[:5], fmt.Sprintf("+%d more", len(names)-5))
		}
		fmt.Fprintf(&b, "   Attendees: %s\n", strings.Join(names, ", "))
	}
	return b.String()
}

func formatEventTime(e *googlecalendar.Event, loc *time.Location) string {
	if e.Start == nil {
		return "unknown"
	}
	if e.Start.DateTime != "" {
		start, _ := time.Parse(time.RFC3339, e.Start.DateTime)
		start = start.In(loc)
		end, _ := time.Parse(time.RFC3339, e.End.DateTime)
		end = end.In(loc)
		return fmt.Sprintf("%s – %s", start.Format("15:04"), end.Format("15:04"))
	}
	// All-day event.
	return "All day (" + e.Start.Date + ")"
}

func parseEventStart(e *googlecalendar.Event, loc *time.Location) time.Time {
	if e.Start.DateTime != "" {
		t, _ := time.Parse(time.RFC3339, e.Start.DateTime)
		return t.In(loc)
	}
	t, _ := time.Parse("2006-01-02", e.Start.Date)
	return t.In(loc)
}

func getMeetLink(e *googlecalendar.Event) string {
	if e.ConferenceData != nil {
		for _, ep := range e.ConferenceData.EntryPoints {
			if ep.EntryPointType == "video" {
				return ep.Uri
			}
		}
	}
	if e.HangoutLink != "" {
		return e.HangoutLink
	}
	return ""
}

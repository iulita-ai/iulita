package google

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"google.golang.org/api/people/v1"

	"github.com/iulita-ai/iulita/internal/skill"
)

// ContactsSkill searches Google Contacts and finds birthdays.
type ContactsSkill struct {
	client *Client
}

func NewContacts(client *Client) *ContactsSkill {
	return &ContactsSkill{client: client}
}

func (s *ContactsSkill) Name() string { return "google_contacts" }

func (s *ContactsSkill) Description() string {
	return "Search Google Contacts by name, email, or phone. Find upcoming birthdays within a date range."
}

func (s *ContactsSkill) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["search", "birthdays", "detail"],
				"description": "Action: search (by name/email/phone), birthdays (upcoming), detail (full contact info)"
			},
			"query": {
				"type": "string",
				"description": "Search query for 'search' action"
			},
			"resource_name": {
				"type": "string",
				"description": "Contact resource name for 'detail' action (e.g. 'people/c123')"
			},
			"days_ahead": {
				"type": "integer",
				"description": "Days to look ahead for birthdays (default: 30)"
			},
			"account": {
				"type": "string",
				"description": "Google account alias or email"
			}
		},
		"required": ["action"]
	}`)
}

func (s *ContactsSkill) RequiredCapabilities() []string { return []string{"google"} }

type contactsInput struct {
	Action       string `json:"action"`
	Query        string `json:"query"`
	ResourceName string `json:"resource_name"`
	DaysAhead    int    `json:"days_ahead"`
	Account      string `json:"account"`
}

func (s *ContactsSkill) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in contactsInput
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

	srv, err := s.client.GetPeopleService(ctx, userID, in.Account)
	if err != nil {
		return "", fmt.Errorf("creating People service: %w", err)
	}

	switch in.Action {
	case "search":
		if in.Query == "" {
			return "", fmt.Errorf("query is required for search action")
		}
		return s.search(srv, in.Query)
	case "birthdays":
		days := in.DaysAhead
		if days <= 0 {
			days = 30
		}
		return s.birthdays(srv, days)
	case "detail":
		if in.ResourceName == "" {
			return "", fmt.Errorf("resource_name is required for detail action")
		}
		return s.detail(srv, in.ResourceName)
	default:
		return "", fmt.Errorf("unknown action %q", in.Action)
	}
}

func (s *ContactsSkill) search(srv *people.Service, query string) (string, error) {
	// Warmup search (People API requirement).
	srv.People.SearchContacts().Query("").ReadMask("names").Do()

	resp, err := srv.People.SearchContacts().
		Query(query).
		ReadMask("names,emailAddresses,phoneNumbers,organizations").
		PageSize(10).
		Do()
	if err != nil {
		return "", fmt.Errorf("searching contacts: %w", err)
	}

	if len(resp.Results) == 0 {
		return fmt.Sprintf("No contacts found matching %q.", query), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Found %d contact(s):\n\n", len(resp.Results))
	for i, r := range resp.Results {
		p := r.Person
		name := getContactName(p)
		fmt.Fprintf(&b, "%d. **%s**\n", i+1, name)

		for _, email := range p.EmailAddresses {
			fmt.Fprintf(&b, "   Email: %s\n", email.Value)
		}
		for _, phone := range p.PhoneNumbers {
			fmt.Fprintf(&b, "   Phone: %s\n", phone.Value)
		}
		for _, org := range p.Organizations {
			if org.Name != "" {
				title := ""
				if org.Title != "" {
					title = " — " + org.Title
				}
				fmt.Fprintf(&b, "   Org: %s%s\n", org.Name, title)
			}
		}
		if p.ResourceName != "" {
			fmt.Fprintf(&b, "   [resource: %s]\n", p.ResourceName)
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

func (s *ContactsSkill) birthdays(srv *people.Service, daysAhead int) (string, error) {
	// Load all connections with birthday data.
	var allPeople []*people.Person
	pageToken := ""
	for {
		call := srv.People.Connections.List("people/me").
			PersonFields("names,birthdays,emailAddresses").
			PageSize(500)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		resp, err := call.Do()
		if err != nil {
			return "", fmt.Errorf("listing contacts: %w", err)
		}
		allPeople = append(allPeople, resp.Connections...)
		pageToken = resp.NextPageToken
		if pageToken == "" {
			break
		}
	}

	now := time.Now()
	type bday struct {
		name      string
		month     int
		day       int
		daysUntil int
	}

	var upcoming []bday
	for _, p := range allPeople {
		if len(p.Birthdays) == 0 {
			continue
		}
		bd := p.Birthdays[0].Date
		if bd == nil || bd.Month == 0 || bd.Day == 0 {
			continue
		}

		name := getContactName(p)
		daysUntil := daysUntilBirthday(now, int(bd.Month), int(bd.Day))
		if daysUntil <= daysAhead {
			upcoming = append(upcoming, bday{
				name:      name,
				month:     int(bd.Month),
				day:       int(bd.Day),
				daysUntil: daysUntil,
			})
		}
	}

	if len(upcoming) == 0 {
		return fmt.Sprintf("No birthdays in the next %d days.", daysAhead), nil
	}

	// Sort by days until.
	for i := 0; i < len(upcoming)-1; i++ {
		for j := i + 1; j < len(upcoming); j++ {
			if upcoming[j].daysUntil < upcoming[i].daysUntil {
				upcoming[i], upcoming[j] = upcoming[j], upcoming[i]
			}
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Upcoming birthdays (next %d days):\n\n", daysAhead)
	for _, bd := range upcoming {
		dateStr := fmt.Sprintf("%s %d", time.Month(bd.month).String(), bd.day)
		if bd.daysUntil == 0 {
			fmt.Fprintf(&b, "- **%s** — %s (TODAY!)\n", bd.name, dateStr)
		} else if bd.daysUntil == 1 {
			fmt.Fprintf(&b, "- **%s** — %s (tomorrow)\n", bd.name, dateStr)
		} else {
			fmt.Fprintf(&b, "- **%s** — %s (in %d days)\n", bd.name, dateStr, bd.daysUntil)
		}
	}
	return b.String(), nil
}

func (s *ContactsSkill) detail(srv *people.Service, resourceName string) (string, error) {
	p, err := srv.People.Get(resourceName).
		PersonFields("names,emailAddresses,phoneNumbers,birthdays,organizations,addresses,urls").
		Do()
	if err != nil {
		return "", fmt.Errorf("getting contact: %w", err)
	}

	var b strings.Builder
	name := getContactName(p)
	fmt.Fprintf(&b, "**%s**\n\n", name)

	for _, email := range p.EmailAddresses {
		label := email.Type
		if label == "" {
			label = "email"
		}
		fmt.Fprintf(&b, "%s: %s\n", label, email.Value)
	}
	for _, phone := range p.PhoneNumbers {
		label := phone.Type
		if label == "" {
			label = "phone"
		}
		fmt.Fprintf(&b, "%s: %s\n", label, phone.Value)
	}
	for _, org := range p.Organizations {
		if org.Name != "" {
			fmt.Fprintf(&b, "Organization: %s", org.Name)
			if org.Title != "" {
				fmt.Fprintf(&b, " — %s", org.Title)
			}
			b.WriteString("\n")
		}
	}
	for _, addr := range p.Addresses {
		if addr.FormattedValue != "" {
			fmt.Fprintf(&b, "Address: %s\n", addr.FormattedValue)
		}
	}
	if len(p.Birthdays) > 0 {
		bd := p.Birthdays[0].Date
		if bd != nil && bd.Month > 0 && bd.Day > 0 {
			if bd.Year > 0 {
				fmt.Fprintf(&b, "Birthday: %s %d, %d\n", time.Month(bd.Month).String(), bd.Day, bd.Year)
			} else {
				fmt.Fprintf(&b, "Birthday: %s %d\n", time.Month(bd.Month).String(), bd.Day)
			}
		}
	}
	for _, u := range p.Urls {
		fmt.Fprintf(&b, "URL: %s\n", u.Value)
	}

	return b.String(), nil
}

func getContactName(p *people.Person) string {
	if len(p.Names) > 0 {
		return p.Names[0].DisplayName
	}
	if len(p.EmailAddresses) > 0 {
		return p.EmailAddresses[0].Value
	}
	return "Unknown"
}

func daysUntilBirthday(now time.Time, month, day int) int {
	thisYear := time.Date(now.Year(), time.Month(month), day, 0, 0, 0, 0, now.Location())
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	diff := int(thisYear.Sub(today).Hours() / 24)
	if diff < 0 {
		// Birthday already passed this year — next year.
		nextYear := time.Date(now.Year()+1, time.Month(month), day, 0, 0, 0, 0, now.Location())
		diff = int(nextYear.Sub(today).Hours() / 24)
	}
	return diff
}

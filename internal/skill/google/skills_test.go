package google

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"google.golang.org/api/gmail/v1"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/skill"
)

// ctxWithUserID returns a context with a user ID set via skill.WithUserID.
func ctxWithUserID(userID string) context.Context {
	return skill.WithUserID(context.Background(), userID)
}

// --- Mail Skill Tests ---

func TestMailSkill_Metadata(t *testing.T) {
	s := NewMail(nil)
	if s.Name() != "google_mail" {
		t.Errorf("expected name google_mail, got %s", s.Name())
	}
	if s.Description() == "" {
		t.Error("expected non-empty description")
	}
	caps := s.RequiredCapabilities()
	if len(caps) != 1 || caps[0] != "google" {
		t.Errorf("expected [google], got %v", caps)
	}
}

func TestMailSkill_InputSchema(t *testing.T) {
	s := NewMail(nil)
	schema := s.InputSchema()
	var v map[string]interface{}
	if err := json.Unmarshal(schema, &v); err != nil {
		t.Fatalf("invalid schema JSON: %v", err)
	}
	if v["type"] != "object" {
		t.Error("expected schema type to be 'object'")
	}
}

func TestMailSkill_NoUserID(t *testing.T) {
	s := NewMail(nil)
	_, err := s.Execute(context.Background(), json.RawMessage(`{"action":"summary"}`))
	if err == nil || !strings.Contains(err.Error(), "user not identified") {
		t.Errorf("expected 'user not identified' error, got: %v", err)
	}
}

func TestMailSkill_InvalidJSON(t *testing.T) {
	s := NewMail(nil)
	_, err := s.Execute(ctxWithUserID("u1"), json.RawMessage(`{bad`))
	if err == nil || !strings.Contains(err.Error(), "invalid input") {
		t.Errorf("expected 'invalid input' error, got: %v", err)
	}
}

func TestMailSkill_NoAccount(t *testing.T) {
	store := &mockTokenStore{}
	c := &Client{store: store}
	s := NewMail(c)

	result, err := s.Execute(ctxWithUserID("u1"), json.RawMessage(`{"action":"summary"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "No Google account connected") {
		t.Errorf("expected 'No Google account' message, got: %s", result)
	}
}

func TestMailSkill_UnknownAction(t *testing.T) {
	store := &mockTokenStore{accounts: testAccounts()}
	c := &Client{store: store, crypto: &mockCrypto{enabled: false}}
	s := NewMail(c)

	_, err := s.Execute(ctxWithUserID("u1"), json.RawMessage(`{"action":"unknown"}`))
	if err == nil || !strings.Contains(err.Error(), "unknown action") {
		t.Errorf("expected 'unknown action' error, got: %v", err)
	}
}

// --- Calendar Skill Tests ---

func TestCalendarSkill_Metadata(t *testing.T) {
	s := NewCalendar(nil, nil)
	if s.Name() != "google_calendar" {
		t.Errorf("expected name google_calendar, got %s", s.Name())
	}
}

func TestCalendarSkill_NoUserID(t *testing.T) {
	s := NewCalendar(nil, nil)
	_, err := s.Execute(context.Background(), json.RawMessage(`{"action":"today"}`))
	if err == nil || !strings.Contains(err.Error(), "user not identified") {
		t.Errorf("expected 'user not identified' error, got: %v", err)
	}
}

func TestCalendarSkill_NoAccount(t *testing.T) {
	store := &mockTokenStore{}
	c := &Client{store: store}
	s := NewCalendar(c, nil)

	result, err := s.Execute(ctxWithUserID("u1"), json.RawMessage(`{"action":"today"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "No Google account connected") {
		t.Errorf("expected 'No Google account' message, got: %s", result)
	}
}

func TestCalendarSkill_UnknownAction(t *testing.T) {
	store := &mockTokenStore{accounts: testAccounts()}
	c := &Client{store: store, crypto: &mockCrypto{enabled: false}}
	s := NewCalendar(c, nil)

	_, err := s.Execute(ctxWithUserID("u1"), json.RawMessage(`{"action":"next_year"}`))
	if err == nil || !strings.Contains(err.Error(), "unknown action") {
		t.Errorf("expected 'unknown action' error, got: %v", err)
	}
}

// --- Contacts Skill Tests ---

func TestContactsSkill_Metadata(t *testing.T) {
	s := NewContacts(nil)
	if s.Name() != "google_contacts" {
		t.Errorf("expected name google_contacts, got %s", s.Name())
	}
}

func TestContactsSkill_NoUserID(t *testing.T) {
	s := NewContacts(nil)
	_, err := s.Execute(context.Background(), json.RawMessage(`{"action":"search","query":"test"}`))
	if err == nil || !strings.Contains(err.Error(), "user not identified") {
		t.Errorf("expected 'user not identified' error, got: %v", err)
	}
}

func TestContactsSkill_NoAccount(t *testing.T) {
	store := &mockTokenStore{}
	c := &Client{store: store}
	s := NewContacts(c)

	result, err := s.Execute(ctxWithUserID("u1"), json.RawMessage(`{"action":"search","query":"test"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "No Google account connected") {
		t.Errorf("expected 'No Google account' message, got: %s", result)
	}
}

func TestContactsSkill_SearchWithoutQuery(t *testing.T) {
	store := &mockTokenStore{accounts: testAccounts()}
	c := &Client{store: store, crypto: &mockCrypto{enabled: false}}
	s := NewContacts(c)

	_, err := s.Execute(ctxWithUserID("u1"), json.RawMessage(`{"action":"search"}`))
	if err == nil || !strings.Contains(err.Error(), "query is required") {
		t.Errorf("expected 'query is required' error, got: %v", err)
	}
}

func TestContactsSkill_DetailWithoutResourceName(t *testing.T) {
	store := &mockTokenStore{accounts: testAccounts()}
	c := &Client{store: store, crypto: &mockCrypto{enabled: false}}
	s := NewContacts(c)

	_, err := s.Execute(ctxWithUserID("u1"), json.RawMessage(`{"action":"detail"}`))
	if err == nil || !strings.Contains(err.Error(), "resource_name is required") {
		t.Errorf("expected 'resource_name is required' error, got: %v", err)
	}
}

// --- Tasks Skill Tests ---

func TestTasksSkill_Metadata(t *testing.T) {
	s := NewTasks(nil)
	if s.Name() != "google_tasks" {
		t.Errorf("expected name google_tasks, got %s", s.Name())
	}
}

func TestTasksSkill_NoUserID(t *testing.T) {
	s := NewTasks(nil)
	_, err := s.Execute(context.Background(), json.RawMessage(`{"action":"lists"}`))
	if err == nil || !strings.Contains(err.Error(), "user not identified") {
		t.Errorf("expected 'user not identified' error, got: %v", err)
	}
}

func TestTasksSkill_NoAccount(t *testing.T) {
	store := &mockTokenStore{}
	c := &Client{store: store}
	s := NewTasks(c)

	result, err := s.Execute(ctxWithUserID("u1"), json.RawMessage(`{"action":"lists"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "No Google account connected") {
		t.Errorf("expected 'No Google account' message, got: %s", result)
	}
}

func TestTasksSkill_CreateWithoutTitle(t *testing.T) {
	store := &mockTokenStore{accounts: testAccounts()}
	c := &Client{store: store, crypto: &mockCrypto{enabled: false}}
	s := NewTasks(c)

	_, err := s.Execute(ctxWithUserID("u1"), json.RawMessage(`{"action":"create"}`))
	if err == nil || !strings.Contains(err.Error(), "title is required") {
		t.Errorf("expected 'title is required' error, got: %v", err)
	}
}

func TestTasksSkill_CompleteWithoutTaskID(t *testing.T) {
	store := &mockTokenStore{accounts: testAccounts()}
	c := &Client{store: store, crypto: &mockCrypto{enabled: false}}
	s := NewTasks(c)

	_, err := s.Execute(ctxWithUserID("u1"), json.RawMessage(`{"action":"complete"}`))
	if err == nil || !strings.Contains(err.Error(), "task_id is required") {
		t.Errorf("expected 'task_id is required' error, got: %v", err)
	}
}

func TestTasksSkill_DeleteWithoutTaskID(t *testing.T) {
	store := &mockTokenStore{accounts: testAccounts()}
	c := &Client{store: store, crypto: &mockCrypto{enabled: false}}
	s := NewTasks(c)

	_, err := s.Execute(ctxWithUserID("u1"), json.RawMessage(`{"action":"delete"}`))
	if err == nil || !strings.Contains(err.Error(), "task_id is required") {
		t.Errorf("expected 'task_id is required' error, got: %v", err)
	}
}

// --- Helper Function Tests ---

func TestDaysUntilBirthday_Today(t *testing.T) {
	now := time.Date(2024, 3, 15, 12, 0, 0, 0, time.UTC)
	days := daysUntilBirthday(now, 3, 15)
	if days != 0 {
		t.Errorf("expected 0 days until birthday today, got %d", days)
	}
}

func TestDaysUntilBirthday_Tomorrow(t *testing.T) {
	now := time.Date(2024, 3, 15, 12, 0, 0, 0, time.UTC)
	days := daysUntilBirthday(now, 3, 16)
	if days != 1 {
		t.Errorf("expected 1 day, got %d", days)
	}
}

func TestDaysUntilBirthday_NextYear(t *testing.T) {
	now := time.Date(2024, 3, 15, 12, 0, 0, 0, time.UTC)
	days := daysUntilBirthday(now, 3, 10) // Already passed
	if days <= 0 || days > 366 {
		t.Errorf("expected positive days (next year), got %d", days)
	}
}

func TestDayRange(t *testing.T) {
	now := time.Date(2024, 3, 15, 14, 30, 0, 0, time.UTC)
	start, end := dayRange(now, 0)

	if start.Hour() != 0 || start.Minute() != 0 {
		t.Error("expected start at midnight")
	}
	if end.Sub(start) != 24*time.Hour {
		t.Error("expected 24 hour range")
	}

	startTomorrow, _ := dayRange(now, 1)
	if startTomorrow.Day() != 16 {
		t.Errorf("expected day 16, got %d", startTomorrow.Day())
	}
}

func TestExtractBody_PlainText(t *testing.T) {
	body := extractBody(&gmail.MessagePart{
		MimeType: "text/plain",
		Body: &gmail.MessagePartBody{
			Data: "SGVsbG8gV29ybGQ=", // "Hello World" base64url
		},
	})
	if body != "Hello World" {
		t.Errorf("expected 'Hello World', got %q", body)
	}
}

func TestExtractBody_Multipart(t *testing.T) {
	body := extractBody(&gmail.MessagePart{
		MimeType: "multipart/alternative",
		Parts: []*gmail.MessagePart{
			{
				MimeType: "text/plain",
				Body: &gmail.MessagePartBody{
					Data: "UGxhaW4gdGV4dA==", // "Plain text"
				},
			},
			{
				MimeType: "text/html",
				Body: &gmail.MessagePartBody{
					Data: "PGI+SFRNTDWVYJ4=", // "<b>HTML</b>"
				},
			},
		},
	})
	if body != "Plain text" {
		t.Errorf("expected 'Plain text', got %q", body)
	}
}

func TestExtractBody_Empty(t *testing.T) {
	body := extractBody(nil)
	if body != "" {
		t.Errorf("expected empty string, got %q", body)
	}
}

func TestGetUserTimezone_Empty(t *testing.T) {
	tz := GetUserTimezone(context.Background(), &mockUserStore{}, "")
	if tz != "UTC" {
		t.Errorf("expected UTC, got %s", tz)
	}
}

func TestManifest(t *testing.T) {
	m, err := LoadManifest()
	if err != nil {
		t.Fatalf("failed to load manifest: %v", err)
	}
	if m.Name == "" {
		t.Error("expected non-empty manifest name")
	}
	if len(m.Capabilities) == 0 {
		t.Error("expected non-empty capabilities")
	}
}

// --- Test helpers ---

func testAccounts() []domain.GoogleAccount {
	return []domain.GoogleAccount{
		{
			ID:                    1,
			UserID:                "u1",
			AccountEmail:          "test@google.com",
			IsDefault:             true,
			EncryptedAccessToken:  "test-access",
			EncryptedRefreshToken: "test-refresh",
			TokenExpiry:           time.Now().Add(time.Hour),
		},
	}
}

// mockUserStore for GetUserTimezone tests.
type mockUserStore struct{}

func (m *mockUserStore) GetUser(_ context.Context, _ string) (*domain.User, error) {
	return nil, context.DeadlineExceeded
}

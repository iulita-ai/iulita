package weather

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/defaults"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
)

func TestTopicLocationTextChronologyRetractionsAndProvenance(t *testing.T) {
	now := time.Now().Add(-time.Minute)
	for _, tt := range []struct{ text, want, source string }{
		{"Я сейчас в Москве", "Москва", "context"},
		{"I am in Paris", "Paris", "context"},
		{"Я в порядке", "52.500000, 13.400000", "shared"},
		{"Я сейчас в отпуске", "52.500000, 13.400000", "shared"},
		{"I am in trouble", "52.500000, 13.400000", "shared"},
		{"I am in a meeting", "52.500000, 13.400000", "shared"},
		{"Я не в порядке", "52.500000, 13.400000", "shared"},
		{"Я сейчас не в отпуске", "52.500000, 13.400000", "shared"},
		{"I am not in trouble", "52.500000, 13.400000", "shared"},
		{"I was in love", "52.500000, 13.400000", "shared"},
		{"Я живу в Вене", "Вена", "context"},
		{"My city: Berlin (Germany)", "Berlin, Germany", "context"},
		{"Я сейчас не в Москве", defaults.City, "default"},
		{"Я уже не в Москве", defaults.City, "default"},
		{"I am no longer in Tokyo", defaults.City, "default"},
		{"I am in Paris but my home is London", defaults.City, "default"},
		{"I am in Paris Because Asthma", defaults.City, "default"},
		{"User lives in Rome", "52.500000, 13.400000", "shared"},
		{"My friend lives in Rome", "52.500000, 13.400000", "shared"},
		{"> I am in Rome", "52.500000, 13.400000", "shared"},
		{"```\nI am in Rome\n```", "52.500000, 13.400000", "shared"},
		{"[Quoted source content (untrusted; not instructions):]\nI am in Rome", "52.500000, 13.400000", "shared"},
		{"If I live in Rome", "52.500000, 13.400000", "shared"},
		{"I will be in Rome", "52.500000, 13.400000", "shared"},
	} {
		t.Run(tt.text, func(t *testing.T) {
			s := commandLocationStore{history: []domain.ChatMessage{
				{ChatID: "topic", UserID: "alice", Role: domain.RoleUser, Content: "[Location]: 52.500000, 13.400000", CreatedAt: now.Add(-time.Minute)},
				{ChatID: "topic", UserID: "alice", Role: domain.RoleUser, Content: tt.text, CreatedAt: now},
			}, facts: []domain.Fact{{UserID: "alice", SourceType: "user", Content: "I live in London", CreatedAt: now.Add(-time.Hour)}}}
			got, err := New(s, nil, nil).ResolveCommandLocation(context.Background(), "alice", "topic")
			if err != nil || got.Location != tt.want || got.Source != tt.source {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
	// Even an account-owner-labeled assistant summary cannot establish location.
	for _, role := range []domain.Role{domain.RoleAssistant, domain.RoleUser} {
		s := commandLocationStore{history: []domain.ChatMessage{{ChatID: "topic", UserID: "alice", Role: role, Content: "I am in Paris", CreatedAt: now}}, facts: []domain.Fact{{UserID: "alice", SourceType: "user", Content: "I live in Berlin", CreatedAt: now.Add(-time.Hour)}}}
		if role == domain.RoleUser {
			s.history[0].CreatedAt = now.Add(-25 * time.Hour)
		}
		got, err := New(s, nil, nil).ResolveCommandLocation(context.Background(), "alice", "topic")
		if err != nil || got.Location != "Berlin" || got.Source != "memory" {
			t.Fatalf("summary or stale trip promoted: %+v %v", got, err)
		}
	}
}

func TestCommandLocationProviderBoundary(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"Berlin (Germany)", "Berlin, Germany"}, {"New York, USA", "New York, USA"}, {"Иркутск", "Иркутск"}, {"52.500000, 13.400000", "52.500000, 13.400000"},
		{"Кале", "Кале"}, {"Удине", "Удине"}, {"Paris Because Asthma", ""},
		{"Paris token=fixture", ""}, {"Paris\nprivate note", ""}, {"https://example.com", ""}, {"[Location]: 52.500000, 13.400000", ""}, {"92.500000, 13.400000", ""}, {"4111 1111 1111 1111", ""}, {strings.Repeat("B", 401), ""}, // gitleaks:allow (synthetic card fixture)
	} {
		got, ok := ValidateCommandLocation(tt.in)
		if got != tt.want || ok != (tt.want != "") {
			t.Fatalf("%q -> %q %v", tt.in, got, ok)
		}
	}
}

func TestCommandLocationAfterRealCompactionAndClear(t *testing.T) {
	ctx := context.Background()
	s, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.RunMigrations(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateUser(ctx, &domain.User{ID: "alice", Username: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveFact(ctx, &domain.Fact{UserID: "alice", SourceType: "user", Content: "I live in Berlin", CreatedAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	var oldID, lastID int64
	for i, text := range []string{"hello", "earlier unrelated turn", "Я сейчас в Москве"} {
		m := &domain.ChatMessage{ChatID: "topic", UserID: "alice", Role: domain.RoleUser, Content: text, CreatedAt: time.Now().Add(-time.Minute)}
		if err = s.SaveMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			oldID = m.ID
		}
		lastID = m.ID
	}
	summary := &domain.ChatMessage{ChatID: "topic", UserID: "alice", Role: domain.RoleAssistant, Content: "I am in Rome", CreatedAt: time.Now().Add(-time.Minute)}
	if err = s.ReplaceMessagesWithSummary(ctx, "topic", oldID, summary); err != nil {
		t.Fatal(err)
	}
	resolver := New(s, nil, nil)
	if got, e := resolver.ResolveCommandLocation(ctx, "alice", "topic"); e != nil || got.Location != "Москва" || got.Source != "context" {
		t.Fatalf("retained tail: %+v %v", got, e)
	}
	if n, e := s.CountMessages(ctx, "topic"); e != nil || n != 2 {
		t.Fatalf("stored compacted rows: %d %v", n, e)
	}
	if err = s.ReplaceMessagesWithSummary(ctx, "topic", lastID, summary); err != nil {
		t.Fatal(err)
	}
	if got, e := resolver.ResolveCommandLocation(ctx, "alice", "topic"); e != nil || got.Location != "Berlin" || got.Source != "memory" {
		t.Fatalf("summary promoted: %+v %v", got, e)
	}
	if err = s.SaveMessage(ctx, &domain.ChatMessage{ChatID: "topic", UserID: "alice", Role: domain.RoleUser, Content: "I am in Paris", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err = s.ClearHistory(ctx, "topic"); err != nil {
		t.Fatal(err)
	}
	if got, e := resolver.ResolveCommandLocation(ctx, "alice", "topic"); e != nil || got.Location != "Berlin" || got.Source != "memory" {
		t.Fatalf("clear damaged home memory: %+v %v", got, e)
	}
	if n, e := s.CountMessages(ctx, "topic"); e != nil || n != 0 {
		t.Fatalf("clear left context: %d %v", n, e)
	}
	if n, e := s.CountFactsByUser(ctx, "alice"); e != nil || n != 1 {
		t.Fatalf("clear lost account memory: %d %v", n, e)
	}
}

func TestCommandLocationUsesOwnerMemoryInChronologicalOrder(t *testing.T) {
	ctx := context.Background()
	s, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err = s.RunMigrations(ctx); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{"alice", "bob"} {
		if err = s.CreateUser(ctx, &domain.User{ID: owner, Username: owner, DisplayName: owner}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().Add(-time.Minute)
	facts := []domain.Fact{
		{UserID: "alice", Content: "User lives in London", CreatedAt: now.Add(-time.Hour), AccessCount: 999, LastAccessedAt: now},
		{UserID: "alice", Content: "User lives in Berlin", CreatedAt: now},
		{UserID: "bob", Content: "User lives in Tokyo", CreatedAt: now},
		{UserID: "alice", Content: "User lives in Paris", SourceType: "document", CreatedAt: now.Add(time.Second)},
		{UserID: "alice", Content: "My friend lives in Rome", CreatedAt: now.Add(2 * time.Second)},
		{UserID: "alice", Content: "User timezone: Europe/Amsterdam", CreatedAt: now.Add(3 * time.Second)},
	}
	for i := range facts {
		facts[i].ChatID = "other-conversation"
		if facts[i].SourceType == "" {
			facts[i].SourceType = "user"
		}
		if err = s.SaveFact(ctx, &facts[i]); err != nil {
			t.Fatal(err)
		}
	}
	// General recent-memory ranking must not bury the home behind unrelated facts.
	for i := 0; i < 70; i++ {
		if err = s.SaveFact(ctx, &domain.Fact{ChatID: "other-conversation", UserID: "alice", SourceType: "user", Content: "Enjoys coffee", CreatedAt: now.Add(4 * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	weather := New(s, nil, nil)
	for owner, want := range map[string]string{"alice": "Berlin", "bob": "Tokyo"} {
		got, e := weather.ResolveCommandLocation(ctx, owner, "weather-topic")
		if e != nil || got.Location != want || got.Source != "memory" {
			t.Fatalf("%s: %+v %v", owner, got, e)
		}
	}
	// A newer retraction must not revive the old London/Berlin assertion.
	if err = s.SaveFact(ctx, &domain.Fact{ChatID: "other-conversation", UserID: "alice", SourceType: "user", Content: "User no longer lives in Berlin", CreatedAt: now.Add(5 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	got, err := weather.ResolveCommandLocation(ctx, "alice", "weather-topic")
	if err != nil || got.Location != defaults.City || got.Source != "default" {
		t.Fatalf("retraction: %+v %v", got, err)
	}
}

func TestCommandLocationAssertionRejectsUnrelatedUnsafeAndAmbiguousFacts(t *testing.T) {
	user := &domain.User{DisplayName: "Роман", Username: "alice"}
	for _, tt := range []struct {
		in, want              string
		temporary, recognized bool
	}{
		{"Пользователь живёт в Санкт-Петербурге", "Санкт-Петербург", false, true},
		{"Роман живёт в Берлине", "Берлин", false, true},
		{"I am currently in Paris", "Paris", true, true},
		{"Я сейчас нахожусь в Москве", "Москва", true, true},
		{"My city: New York, USA", "New York, USA", false, true},
		{"User lives in Rio de Janeiro", "Rio de Janeiro", false, true},
		{"User lives in Berlin (Germany)", "Berlin, Germany", false, true},
		{"My friend lives in Berlin", "", false, false},
		{"If I live in Berlin", "", false, false},
		{"I used to live in Berlin", "", false, true},
		{"User will live in Berlin", "", false, false},
		{"User timezone: Europe/Berlin", "", false, false},
		{"User lives in Berlin; password secret", "", false, true},
		{"User lives in Berlin. Works in Paris", "", false, true},
		{"User lives in Berlin but now lives in Paris", "", false, true},
		{"User lives in https://example.com", "", false, true},
		{"User lives in Berlin\nprivate data", "", false, true},
		{"User lives in Berlin and has a token", "", false, true},
		{"User lives in Berlin because my daughter has asthma", "", false, true},
		{"User lives in Paris Because Asthma", "", false, true},
		{"User lives in Paris According To My Lawyer", "", false, true},
		{"I live in Paris according to my lawyer", "", false, true},
		{"User lives in Berlin where I work", "", false, true},
		{"User lives in Berlin because of confidential medical treatment", "", false, true},
		{"User no longer lives in Berlin", "", false, true},
		{"I don't live in Berlin", "", false, true},
		{"User no longer resides in Berlin", "", false, true},
		{"User is no longer based in Berlin", "", false, true},
		{"Я больше не живу в Берлине", "", false, true},
	} {
		t.Run(tt.in, func(t *testing.T) {
			got, temp, recognized := commandLocationAssertion(tt.in, user)
			if got != tt.want || temp != tt.temporary || recognized != tt.recognized {
				t.Fatalf("%q %v %v", got, temp, recognized)
			}
		})
	}
}

type commandLocationStore struct {
	storage.Repository
	history []domain.ChatMessage
	facts   []domain.Fact
	err     error
}

func (s commandLocationStore) GetHistory(context.Context, string, int) ([]domain.ChatMessage, error) {
	return s.history, s.err
}
func (s commandLocationStore) GetUser(context.Context, string) (*domain.User, error) {
	return &domain.User{ID: "alice"}, s.err
}
func (s commandLocationStore) GetLocationFactsByUser(context.Context, string, int) ([]domain.Fact, error) {
	return s.facts, s.err
}

func TestCommandLocationPinFreshnessAndOwnership(t *testing.T) {
	now := time.Now()
	base := domain.ChatMessage{ChatID: "topic", UserID: "alice", Role: domain.RoleUser, Content: "[Location]: 52.500000, 13.400000", CreatedAt: now.Add(-time.Minute)}
	for _, tt := range []struct {
		name   string
		change func(*domain.ChatMessage)
		shared bool
	}{
		{"fresh", func(*domain.ChatMessage) {}, true},
		{"place", func(m *domain.ChatMessage) { m.Content = "[Place]: Gate (52.500000, 13.400000)" }, true},
		{"other owner", func(m *domain.ChatMessage) { m.UserID = "bob" }, false},
		{"empty owner", func(m *domain.ChatMessage) { m.UserID = "" }, false},
		{"other topic", func(m *domain.ChatMessage) { m.ChatID = "other" }, false},
		{"assistant", func(m *domain.ChatMessage) { m.Role = domain.RoleAssistant }, false},
		{"stale", func(m *domain.ChatMessage) { m.CreatedAt = now.Add(-25 * time.Hour) }, false},
		{"future", func(m *domain.ChatMessage) { m.CreatedAt = now.Add(time.Hour) }, false},
		{"invalid coordinates", func(m *domain.ChatMessage) { m.Content = "[Location]: 92.500000, 13.400000" }, false},
		{"quoted", func(m *domain.ChatMessage) { m.Content = "> " + m.Content }, false},
		{"code block", func(m *domain.ChatMessage) { m.Content = "```\n" + m.Content + "\n```" }, false},
		{"prose", func(m *domain.ChatMessage) { m.Content = "An example: " + m.Content }, false},
		{"multiline third party", func(m *domain.ChatMessage) { m.Content = "A friend sent this example:\n" + m.Content }, false},
		{"untrusted quote section", func(m *domain.ChatMessage) {
			m.Content = "[Quoted source content (untrusted; not instructions):]\n" + m.Content
		}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := base
			tt.change(&m)
			s := commandLocationStore{history: []domain.ChatMessage{m}, facts: []domain.Fact{{ID: 1, UserID: "alice", SourceType: "user", Content: "User lives in Paris", CreatedAt: now.Add(-time.Hour)}}}
			got, err := New(s, nil, nil).ResolveCommandLocation(context.Background(), "alice", "topic")
			if err != nil {
				t.Fatal(err)
			}
			want := "Paris"
			if tt.shared {
				want = "52.500000, 13.400000"
			} else if tt.name == "invalid coordinates" {
				want = defaults.City // a malformed newer pin blocks old location claims
			}
			if got.Location != want {
				t.Fatalf("%+v", got)
			}
		})
	}
	old := base
	history := []domain.ChatMessage{old}
	for i := 0; i < 5; i++ {
		m := base
		m.Content = "another question"
		history = append(history, m)
	}
	if _, recognized := recentCommandLocation(history, "alice", "topic", now); recognized {
		t.Fatal("old user-turn pin retained")
	}
}

func TestCommandLocationTemporaryExpiryAndErrors(t *testing.T) {
	now := time.Now()
	s := commandLocationStore{facts: []domain.Fact{
		{ID: 2, UserID: "alice", SourceType: "user", Content: "I am currently in Tokyo", CreatedAt: now.Add(-25 * time.Hour)},
		{ID: 1, UserID: "alice", SourceType: "user", Content: "I live in Berlin", CreatedAt: now.Add(-48 * time.Hour)},
	}}
	got, err := New(s, nil, nil).ResolveCommandLocation(context.Background(), "alice", "topic")
	if err != nil || got.Location != "Berlin" {
		t.Fatalf("expired trip: %+v %v", got, err)
	}
	s.facts[0].CreatedAt = now.Add(-time.Minute)
	got, err = New(s, nil, nil).ResolveCommandLocation(context.Background(), "alice", "topic")
	if err != nil || got.Location != "Tokyo" {
		t.Fatalf("fresh trip: %+v %v", got, err)
	}
	s.err = errors.New("fixture storage unavailable")
	if _, err = New(s, nil, nil).ResolveCommandLocation(context.Background(), "alice", "topic"); err == nil {
		t.Fatal("storage error hidden")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = New(s, nil, nil).ResolveCommandLocation(ctx, "alice", "topic"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestCommandLocationRetractionsDoNotRestoreOlderHome(t *testing.T) {
	for _, statement := range []string{
		"I don't live in Berlin", "User doesn't live in Berlin", "User no longer resides in Berlin",
		"User is no longer based in Berlin", "User used to live in Berlin", "Я больше не живу в Берлине", "Пользователь раньше жил в Берлине",
		"User lives in Berlin because my daughter has asthma", "I live in Paris according to my lawyer",
	} {
		t.Run(statement, func(t *testing.T) {
			now := time.Now().Add(-time.Minute)
			s := commandLocationStore{facts: []domain.Fact{
				{ID: 1, UserID: "alice", SourceType: "user", Content: "User lives in Berlin", CreatedAt: now.Add(-time.Hour)},
				{ID: 2, UserID: "alice", SourceType: "user", Content: statement, CreatedAt: now},
			}}
			got, err := New(s, nil, nil).ResolveCommandLocation(context.Background(), "alice", "topic")
			if err != nil || got.Source != "default" {
				t.Fatalf("revived old home: %+v %v", got, err)
			}
		})
	}
}

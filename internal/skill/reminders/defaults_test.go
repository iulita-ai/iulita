package reminders

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
)

func TestReminderDefaultAndExplicitTimezonePersisted(t *testing.T) {
	db, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := skill.WithChatID(context.Background(), "reminder-fixture")
	if err = db.RunMigrations(ctx); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ zone, storedZone, due, utc string }{
		{"", "Asia/Irkutsk", "2030-01-02T10:00:00", "2030-01-02T02:00:00Z"},
		{"UTC", "UTC", "2030-01-02T10:00:00", "2030-01-02T10:00:00Z"},
		{"", "Asia/Irkutsk", "2030-01-02T10:00:00+03:00", "2030-01-02T07:00:00Z"},
	} {
		raw, err := json.Marshal(input{Action: "create", Title: tc.utc, DueAt: tc.due, Timezone: tc.zone})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = New(db).Execute(ctx, raw); err != nil {
			t.Fatal(err)
		}
		rows, err := db.ListReminders(ctx, "reminder-fixture")
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, row := range rows {
			if row.Title == tc.utc {
				found = row.Timezone == tc.storedZone && row.DueAt.UTC().Format(time.RFC3339) == tc.utc
			}
		}
		if !found {
			t.Fatalf("reminder not saved with expected instant and timezone: %+v", rows)
		}
	}
}

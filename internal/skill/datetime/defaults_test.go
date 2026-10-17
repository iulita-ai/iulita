package datetime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDefaultAndExplicitTimezones(t *testing.T) {
	for _, tc := range []struct {
		input, zone string
	}{
		{`{}`, "Asia/Irkutsk"},
		{`{"timezone":" "}`, "Asia/Irkutsk"},
		{`{"timezone":"UTC"}`, "UTC"},
		{`{"timezone":"Europe/Moscow"}`, "Europe/Moscow"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			before := time.Now()
			out, err := New().Execute(context.Background(), json.RawMessage(tc.input))
			if err != nil || !strings.Contains(out, "Timezone: "+tc.zone+"\n") {
				t.Fatalf("timezone: %s, %v", out, err)
			}
			lines := strings.Split(out, "\n")
			loc, err := time.LoadLocation(tc.zone)
			if err != nil {
				t.Fatal(err)
			}
			got, err := time.ParseInLocation("2006-01-02 15:04:05", strings.TrimPrefix(lines[0], "Date: ")+" "+strings.TrimPrefix(lines[1], "Time: "), loc)
			if err != nil || got.Before(before.Add(-time.Second)) || got.After(time.Now()) {
				t.Fatalf("wrong local time: %s, %v", out, err)
			}
		})
	}
	if _, err := New().Execute(context.Background(), json.RawMessage(`{"timezone":"Unknown/Zone"}`)); err == nil {
		t.Fatal("invalid explicit timezone silently replaced")
	}
}

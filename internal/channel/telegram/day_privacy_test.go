package telegram

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/iulita-ai/iulita/internal/dayview"
)

func TestDayStateSanitizedBeforePersistence(t *testing.T) {
	card := "4111111111111111"
	token := "sk-proj-" + strings.Repeat("a", 30)
	sections := []dayview.Section{{Name: "todoist", Items: []dayview.Item{{Title: token + " " + card, Due: card, Deadline: token + " " + card}}}}
	sanitizeDaySections(sections)
	raw, e := json.Marshal(sections)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(raw), card) || strings.Contains(string(raw), token) {
		t.Fatal("raw card persisted")
	}
}

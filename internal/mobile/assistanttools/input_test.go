package assistanttools

import (
	"encoding/json"
	"testing"

	"github.com/iulita-ai/iulita/internal/skill/google"
	"github.com/iulita-ai/iulita/internal/skill/memory"
)

func TestNativeInputsRejectIdentityOverridesAndMalformedActions(t *testing.T) {
	schema := google.NewTasks(nil).InputSchema()
	for _, input := range []string{`null`, `[]`, `{"action":"list","user_id":"other"}`, `{"action":"list","account":"x","account":"y"}`, `{"action":"list","show_completed":"false"}`, `{"action":"send_mail"}`, `{"title":"no action"}`, `{"action":"list"} {}`} {
		if validShape(schema, json.RawMessage(input)) {
			t.Errorf("accepted unsafe input %s", input)
		}
	}
	if !validShape(schema, json.RawMessage(`{"action":"create","title":"Тест","show_completed":false}`)) {
		t.Fatal("valid native input rejected")
	}
	if validInput("recall", memory.NewRecall(nil).InputSchema(), json.RawMessage(`{"query":" ","limit":500}`)) {
		t.Fatal("unbounded recall")
	}
}

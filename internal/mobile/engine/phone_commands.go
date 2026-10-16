package engine

import (
	"encoding/json"
	"strings"

	"github.com/google/uuid"
	"github.com/iulita-ai/iulita/internal/llm"
)

// Parse only the current immutable user message, never model output/history.
// These three fixed read requests still go through the normal pinned tool,
// authority, journal, consent and native-session checks in process.
func phoneCommand(input, target string) (call llm.ToolCall, handled bool, problem string) {
	input = strings.TrimSpace(input)
	parts := strings.Fields(input)
	if len(parts) == 0 || parts[0] != "/phone" {
		return llm.ToolCall{}, false, ""
	}
	name := map[string]string{"/phone status": "phone_status", "/phone location": "phone_location", "/phone notifications": "phone_notifications"}[input]
	if name == "" {
		return llm.ToolCall{}, true, "Неизвестный запрос данных телефона."
	}
	if _, err := uuid.Parse(target); err != nil {
		return llm.ToolCall{}, true, "Для запроса нужно выбрать свой телефон."
	}
	body, _ := json.Marshal(map[string]string{"device_id": target}) //nolint:errcheck // Fixed string-only input.
	return llm.ToolCall{ID: "explicit-phone-read", Name: name, Input: body}, true, ""
}

func phoneCommandAcknowledgement(outcome string) (string, bool) {
	var result struct {
		TaskID string `json:"task_id"`
		State  string `json:"state"`
	}
	decoder := json.NewDecoder(strings.NewReader(outcome))
	decoder.DisallowUnknownFields()
	if !json.Valid([]byte(outcome)) || decoder.Decode(&result) != nil || result.State != "queued" {
		return "", false
	}
	if _, err := uuid.Parse(result.TaskID); err != nil {
		return "", false
	}
	return "Запрос создан. Результат можно будет открыть в карточке данных телефона.", true
}

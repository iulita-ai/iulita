package importer

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStreamConversationsIteration(t *testing.T) {
	data := `[{"uuid":"a"},{"uuid":"b"},{"uuid":"c"}]`
	var seen []string
	err := StreamConversations(strings.NewReader(data), func(raw json.RawMessage) error {
		var c struct {
			UUID string `json:"uuid"`
		}
		if err := json.Unmarshal(raw, &c); err != nil {
			return err
		}
		seen = append(seen, c.UUID)
		return nil
	})
	if err != nil {
		t.Fatalf("StreamConversations: %v", err)
	}
	if strings.Join(seen, ",") != "a,b,c" {
		t.Fatalf("expected a,b,c got %v", seen)
	}
}

func TestStreamConversationsRejectsNonArray(t *testing.T) {
	err := StreamConversations(strings.NewReader(`{"uuid":"a"}`), func(json.RawMessage) error { return nil })
	if err == nil {
		t.Fatal("expected error for non-array top-level JSON")
	}
}

func TestMapConversationOrderingAndSeq(t *testing.T) {
	// Deliberately out of order in the array; one message reconstructs empty and is skipped.
	raw := []byte(`{
		"uuid":"conv-1","name":"Planning","summary":"a summary",
		"account":{"uuid":"acc-1"},
		"created_at":"2025-01-27T19:23:47.398804Z",
		"updated_at":"2025-01-28T10:00:00.000000Z",
		"chat_messages":[
			{"uuid":"m2","sender":"assistant","text":"second","created_at":"2025-01-27T19:24:00.000000Z","parent_message_uuid":"m1"},
			{"uuid":"m1","sender":"human","text":"first","created_at":"2025-01-27T19:23:50.000000Z"},
			{"uuid":"m3","sender":"human","text":"   ","created_at":"2025-01-27T19:25:00.000000Z"}
		]
	}`)
	res, err := MapConversation(raw, "admin", 0)
	if err != nil {
		t.Fatalf("MapConversation: %v", err)
	}
	c := res.Conversation
	if c.SourceUUID != "conv-1" || c.UserID != "admin" || c.AccountUUID != "acc-1" {
		t.Fatalf("header mismatch: %+v", c)
	}
	if c.Title != "Planning" || c.Summary != "a summary" {
		t.Fatalf("title/summary mismatch: %+v", c)
	}
	if c.CreatedAt.IsZero() || c.UpdatedAt.IsZero() {
		t.Fatalf("timestamps not parsed: %+v", c)
	}
	if res.SkippedEmpty != 1 {
		t.Fatalf("expected 1 skipped-empty, got %d", res.SkippedEmpty)
	}
	if len(res.Messages) != 2 {
		t.Fatalf("expected 2 stored messages, got %d", len(res.Messages))
	}
	// Sorted by created_at → m1 then m2, with contiguous Seq.
	if res.Messages[0].SourceUUID != "m1" || res.Messages[0].Seq != 0 {
		t.Errorf("message[0] = %+v, want m1 seq 0", res.Messages[0])
	}
	if res.Messages[1].SourceUUID != "m2" || res.Messages[1].Seq != 1 {
		t.Errorf("message[1] = %+v, want m2 seq 1", res.Messages[1])
	}
	if res.Messages[1].ParentMessageUUID != "m1" {
		t.Errorf("parent uuid not preserved: %q", res.Messages[1].ParentMessageUUID)
	}
	if c.MessageCount != 2 {
		t.Errorf("MessageCount = %d, want 2", c.MessageCount)
	}
}

func TestMapConversationTitleFallback(t *testing.T) {
	raw := []byte(`{"uuid":"c","name":"","created_at":"2025-03-15T08:00:00Z","chat_messages":[]}`)
	res, err := MapConversation(raw, "admin", 0)
	if err != nil {
		t.Fatalf("MapConversation: %v", err)
	}
	if res.Conversation.Title != "Untitled (2025-03-15)" {
		t.Errorf("title fallback = %q, want Untitled (2025-03-15)", res.Conversation.Title)
	}
}

func TestMapConversationTolerantTimestamps(t *testing.T) {
	raw := []byte(`{
		"uuid":"c","name":"x","created_at":"not-a-date",
		"chat_messages":[
			{"uuid":"m1","sender":"human","text":"one","created_at":"2025-01-01T00:00:00Z"},
			{"uuid":"m2","sender":"human","text":"two","created_at":"garbage"}
		]
	}`)
	res, err := MapConversation(raw, "admin", 0)
	if err != nil {
		t.Fatalf("MapConversation should tolerate bad timestamps, got %v", err)
	}
	// One bad conversation timestamp + one bad message timestamp = 2 parse errors.
	if res.ParseErrors != 2 {
		t.Errorf("ParseErrors = %d, want 2", res.ParseErrors)
	}
	if len(res.Messages) != 2 {
		t.Errorf("expected both messages stored despite bad timestamp, got %d", len(res.Messages))
	}
	// m2 (bad ts) carries forward m1's time, preserving array order.
	if res.Messages[0].SourceUUID != "m1" || res.Messages[1].SourceUUID != "m2" {
		t.Errorf("carry-forward order broken: %v, %v", res.Messages[0].SourceUUID, res.Messages[1].SourceUUID)
	}
}

func TestMapConversationOversizedGuard(t *testing.T) {
	raw := []byte(`{"uuid":"big","name":"x","chat_messages":[{"uuid":"m1","text":"content","created_at":"2025-01-01T00:00:00Z"}]}`)
	res, err := MapConversation(raw, "admin", 10) // tiny cap forces oversized
	if err != nil {
		t.Fatalf("MapConversation oversized: %v", err)
	}
	if !res.Oversized {
		t.Fatal("expected Oversized=true")
	}
	if len(res.Messages) != 0 {
		t.Fatalf("oversized element must not map messages, got %d", len(res.Messages))
	}
	if res.Conversation.SourceUUID != "big" {
		t.Errorf("expected best-effort UUID capture, got %q", res.Conversation.SourceUUID)
	}
}

func TestMapConversationBadJSON(t *testing.T) {
	_, err := MapConversation([]byte(`{"uuid":`), "admin", 0)
	if err == nil {
		t.Fatal("expected unmarshal error for malformed element")
	}
}

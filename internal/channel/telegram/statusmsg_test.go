package telegram

import (
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/channel"
)

func TestStatusState_CreateGetRemove(t *testing.T) {
	ss := newStatusState()

	// Initially empty.
	if _, ok := ss.get("chat1"); ok {
		t.Fatal("expected no entry")
	}

	// Create.
	entry := ss.create("chat1", 12345, 100)
	if entry.tgChatID != 12345 {
		t.Errorf("tgChatID = %d, want 12345", entry.tgChatID)
	}
	if entry.msgID != 100 {
		t.Errorf("msgID = %d, want 100", entry.msgID)
	}

	// Get.
	e2, ok := ss.get("chat1")
	if !ok || e2 != entry {
		t.Fatal("get should return the same entry")
	}

	// Remove.
	ss.remove("chat1")
	if _, ok := ss.get("chat1"); ok {
		t.Fatal("expected entry removed")
	}
}

func TestStatusEntry_AddAndRender(t *testing.T) {
	e := &statusEntry{}
	e.addLine("🔄 Processing...")
	e.addLine("🔍 websearch...")

	got := e.renderText()
	want := "🔄 Processing...\n🔍 websearch..."
	if got != want {
		t.Errorf("renderText = %q, want %q", got, want)
	}
}

func TestStatusEntry_UpdateLastLine(t *testing.T) {
	e := &statusEntry{}
	e.addLine("🔄 Processing...")
	e.addLine("🔍 websearch...")
	e.updateLastLine("🔍 websearch ✅ (2100ms)")

	got := e.renderText()
	want := "🔄 Processing...\n🔍 websearch ✅ (2100ms)"
	if got != want {
		t.Errorf("renderText = %q, want %q", got, want)
	}
}

func TestStatusEntry_UpdateLastLineEmpty(t *testing.T) {
	e := &statusEntry{}
	e.updateLastLine("some line")

	got := e.renderText()
	if got != "some line" {
		t.Errorf("renderText = %q, want %q", got, "some line")
	}
}

func TestStatusEntry_CanEdit(t *testing.T) {
	e := &statusEntry{lastEdit: time.Now()}
	if e.canEdit() {
		t.Error("should not be able to edit immediately")
	}

	e.lastEdit = time.Now().Add(-3 * time.Second)
	if !e.canEdit() {
		t.Error("should be able to edit after 3s")
	}
}

func TestStatusEntry_MarkConsumed(t *testing.T) {
	e := &statusEntry{msgID: 42}
	id := e.markConsumed()
	if id != 42 {
		t.Errorf("markConsumed returned %d, want 42", id)
	}
	if !e.consumed {
		t.Error("expected consumed = true")
	}
}

func TestFormatStatusLine_Processing(t *testing.T) {
	line, replaces := formatStatusLine(channel.StatusEvent{Type: "processing"})
	if line != "🔄 Processing..." {
		t.Errorf("line = %q", line)
	}
	if replaces {
		t.Error("processing should not replace")
	}
}

func TestFormatStatusLine_SkillStart(t *testing.T) {
	line, replaces := formatStatusLine(channel.StatusEvent{Type: "skill_start", SkillName: "websearch"})
	if line != "🔍 websearch..." {
		t.Errorf("line = %q", line)
	}
	if replaces {
		t.Error("skill_start should not replace")
	}
}

func TestFormatStatusLine_SkillDoneSuccess(t *testing.T) {
	line, replaces := formatStatusLine(channel.StatusEvent{
		Type: "skill_done", SkillName: "websearch", Success: true, DurationMs: 2100,
	})
	if line != "🔍 websearch ✅ (2100ms)" {
		t.Errorf("line = %q", line)
	}
	if !replaces {
		t.Error("skill_done should replace previous line")
	}
}

func TestFormatStatusLine_SkillDoneFailed(t *testing.T) {
	line, replaces := formatStatusLine(channel.StatusEvent{
		Type: "skill_done", SkillName: "webfetch", Success: false,
	})
	if line != "📡 webfetch ❌" {
		t.Errorf("line = %q", line)
	}
	if !replaces {
		t.Error("skill_done should replace")
	}
}

func TestFormatStatusLine_OrchestrationStarted(t *testing.T) {
	line, _ := formatStatusLine(channel.StatusEvent{
		Type: "orchestration_started",
		Data: map[string]string{"agent_count": "3"},
	})
	if line != "🤖 Orchestration (3 agents)" {
		t.Errorf("line = %q", line)
	}
}

func TestFormatStatusLine_AgentEvents(t *testing.T) {
	line, _ := formatStatusLine(channel.StatusEvent{
		Type: "agent_started",
		Data: map[string]string{"agent_id": "researcher", "agent_type": "researcher"},
	})
	if line != "  → researcher (researcher)..." {
		t.Errorf("agent_started line = %q", line)
	}

	line, _ = formatStatusLine(channel.StatusEvent{
		Type: "agent_completed",
		Data: map[string]string{"agent_id": "researcher", "duration_ms": "1200"},
	})
	if line != "  → researcher ✅ (1200ms)" {
		t.Errorf("agent_completed line = %q", line)
	}

	line, _ = formatStatusLine(channel.StatusEvent{
		Type: "agent_failed",
		Data: map[string]string{"agent_id": "analyst"},
	})
	if line != "  → analyst ❌" {
		t.Errorf("agent_failed line = %q", line)
	}
}

func TestFormatStatusLine_Error(t *testing.T) {
	line, _ := formatStatusLine(channel.StatusEvent{Type: "error", Error: "timeout"})
	if line != "❌ timeout" {
		t.Errorf("line = %q", line)
	}
}

func TestFormatStatusLine_Unknown(t *testing.T) {
	line, _ := formatStatusLine(channel.StatusEvent{Type: "unknown_type"})
	if line != "" {
		t.Errorf("expected empty for unknown type, got %q", line)
	}
}

func TestSkillEmoji(t *testing.T) {
	tests := map[string]string{
		"websearch":   "🔍",
		"webfetch":    "📡",
		"remember":    "💾",
		"recall":      "🧠",
		"orchestrate": "🤖",
		"tasks":       "📋",
		"weather":     "🌤",
		"shell_exec":  "💻",
		"unknown":     "🔧",
	}
	for skill, want := range tests {
		if got := skillEmoji(skill); got != want {
			t.Errorf("skillEmoji(%q) = %q, want %q", skill, got, want)
		}
	}
}

func TestFullStatusLifecycle(t *testing.T) {
	ss := newStatusState()

	// Simulate: processing → skill_start → skill_done → stream_start.
	entry := ss.create("chat1", 100, 42)
	entry.addLine("🔄 Processing...")

	// skill_start
	entry.addLine("🔍 websearch...")
	want := "🔄 Processing...\n🔍 websearch..."
	if got := entry.renderText(); got != want {
		t.Errorf("after skill_start: %q", got)
	}

	// skill_done (replaces last)
	entry.updateLastLine("🔍 websearch ✅ (2100ms)")
	want = "🔄 Processing...\n🔍 websearch ✅ (2100ms)"
	if got := entry.renderText(); got != want {
		t.Errorf("after skill_done: %q", got)
	}

	// stream_start → consume
	id := entry.markConsumed()
	if id != 42 {
		t.Errorf("consumed msgID = %d, want 42", id)
	}
	if !entry.consumed {
		t.Fatal("expected consumed")
	}

	ss.remove("chat1")
	if _, ok := ss.get("chat1"); ok {
		t.Fatal("entry should be removed")
	}
}

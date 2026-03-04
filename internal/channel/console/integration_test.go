package console

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/iulita-ai/iulita/internal/channel"
)

// --- Integration tests ---
// These test full flows through the TUI model, simulating realistic user interactions.

func TestIntegration_FullChatFlow(t *testing.T) {
	// Setup handler that echoes back.
	handler := func(_ context.Context, msg channel.IncomingMessage) (string, error) {
		return "Echo: " + msg.Text, nil
	}

	m := newModel(context.Background(), handler, "admin-1", "console", "test-inst", nil, nil, "", nil)

	// Initialize with window size.
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(tuiModel)

	if !m.ready {
		t.Fatal("model should be ready")
	}

	// Type and send a message.
	m.textarea.SetValue("Hello iulita")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)

	// Verify user message appended.
	if len(m.messages) != 1 || m.messages[0].role != "user" || m.messages[0].content != "Hello iulita" {
		t.Fatalf("expected user message, got %+v", m.messages)
	}
	if !m.streaming {
		t.Fatal("should be in streaming state")
	}
	if m.textarea.Value() != "" {
		t.Fatal("textarea should be cleared")
	}

	// Execute the handler command (simulates goroutine completion).
	if cmd == nil {
		t.Fatal("expected a command to call handler")
	}
	result := cmd()
	resp, ok := result.(responseMsg)
	if !ok {
		t.Fatalf("expected responseMsg, got %T", result)
	}
	if resp.text != "Echo: Hello iulita" {
		t.Errorf("unexpected response: %q", resp.text)
	}

	// Feed response back into model.
	updated, _ = m.Update(resp)
	m = updated.(tuiModel)

	if m.streaming {
		t.Error("should not be streaming after response")
	}
	if len(m.messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(m.messages))
	}
	if m.messages[1].role != "assistant" || m.messages[1].content != "Echo: Hello iulita" {
		t.Errorf("unexpected assistant message: %+v", m.messages[1])
	}

	// Verify view contains both messages.
	view := m.View()
	if !strings.Contains(view, "Hello iulita") {
		t.Error("view should contain user message")
	}
	if !strings.Contains(view, "Echo") {
		t.Error("view should contain assistant response")
	}
	if m.msgCount != 2 {
		t.Errorf("expected msgCount 2, got %d", m.msgCount)
	}
}

func TestIntegration_StreamingFlow(t *testing.T) {
	// Handler that returns empty (streaming used instead).
	handler := func(_ context.Context, msg channel.IncomingMessage) (string, error) {
		return "", nil
	}

	m := newModel(context.Background(), handler, "admin-1", "console", "test-inst", nil, nil, "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(tuiModel)

	// Send user message.
	m.textarea.SetValue("Tell me a story")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)

	if !m.streaming {
		t.Fatal("should be streaming")
	}

	// Simulate streaming chunks arriving.
	updated, _ = m.Update(streamChunkMsg("Once upon"))
	m = updated.(tuiModel)
	if m.streamBuf != "Once upon" {
		t.Errorf("expected stream buffer 'Once upon', got %q", m.streamBuf)
	}
	updated, _ = m.Update(streamChunkMsg("Once upon a time"))
	m = updated.(tuiModel)
	if m.streamBuf != "Once upon a time" {
		t.Errorf("expected full stream buffer, got %q", m.streamBuf)
	}

	// Stream done.
	updated, _ = m.Update(streamDoneMsg("Once upon a time, the end."))
	m = updated.(tuiModel)
	if m.streaming {
		t.Error("should not be streaming after done")
	}
	if m.streamBuf != "" {
		t.Error("stream buffer should be cleared")
	}
	// Should have user msg + assistant msg.
	if len(m.messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(m.messages))
	}
	if m.messages[1].content != "Once upon a time, the end." {
		t.Errorf("unexpected final message: %q", m.messages[1].content)
	}

	// Now the handler's responseMsg arrives — should NOT double-append.
	if cmd != nil {
		result := cmd()
		updated, _ = m.Update(result)
		m = updated.(tuiModel)
	}
	if len(m.messages) != 2 {
		t.Errorf("double-append detected: expected 2 messages, got %d", len(m.messages))
	}
}

func TestIntegration_SlashCommandFlow(t *testing.T) {
	handlerCalled := false
	handler := func(_ context.Context, msg channel.IncomingMessage) (string, error) {
		handlerCalled = true
		return "got: " + msg.Text, nil
	}

	m := newModel(context.Background(), handler, "admin-1", "console", "test-inst", nil, nil, "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(tuiModel)

	// /help should be handled locally, not sent to handler.
	m.textarea.SetValue("/help")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)
	if handlerCalled {
		t.Error("handler should NOT be called for /help")
	}
	if len(m.messages) == 0 || m.messages[len(m.messages)-1].role != "status" {
		t.Error("expected status message from /help")
	}

	// Regular text should go to handler.
	m.textarea.SetValue("regular message")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)

	if cmd == nil {
		t.Fatal("expected handler command for regular message")
	}
	result := cmd()
	if !handlerCalled {
		t.Error("handler should be called for regular text")
	}
	resp := result.(responseMsg)
	if resp.text != "got: regular message" {
		t.Errorf("unexpected response: %q", resp.text)
	}
}

func TestIntegration_StatusCommand(t *testing.T) {
	sp := &StatusProvider{
		EnabledSkills: func() int { return 12 },
		TotalSkills:   func() int { return 15 },
		DailyCost:     func() float64 { return 0.4567 },
		SessionStats:  func() (int64, int64, int64) { return 1000, 500, 5 },
	}

	m := newModel(context.Background(), nil, "admin-1", "console", "test-inst", sp, nil, "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(tuiModel)

	m.appendMessage("user", "test1")
	m.appendMessage("assistant", "resp1")

	m.textarea.SetValue("/status")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)

	// Find the status output.
	var statusContent string
	for _, msg := range m.messages {
		if msg.role == "status" {
			statusContent = msg.content
			break
		}
	}
	if statusContent == "" {
		t.Fatal("expected status message")
	}
	if !strings.Contains(statusContent, "12 enabled") {
		t.Errorf("should show enabled skills count, got: %s", statusContent)
	}
	if !strings.Contains(statusContent, "3 disabled") {
		t.Errorf("should show disabled skills count, got: %s", statusContent)
	}
	if !strings.Contains(statusContent, "$0.4567") {
		t.Errorf("should show daily cost, got: %s", statusContent)
	}
	if !strings.Contains(statusContent, "5 requests") {
		t.Errorf("should show session requests, got: %s", statusContent)
	}
	if !strings.Contains(statusContent, "Messages (this session): 2") {
		t.Errorf("should show session message count, got: %s", statusContent)
	}
}

func TestIntegration_StatusCommand_NoProvider(t *testing.T) {
	m := newModel(context.Background(), nil, "admin-1", "console", "test-inst", nil, nil, "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(tuiModel)

	m.textarea.SetValue("/status")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)

	var found bool
	for _, msg := range m.messages {
		if msg.role == "status" && strings.Contains(msg.content, "not available") {
			found = true
		}
	}
	if !found {
		t.Error("expected 'not available' message when no status provider")
	}
}

func TestIntegration_CompactCommand(t *testing.T) {
	compactCalled := false
	compactFn := func(_ context.Context, chatID string) (int, error) {
		compactCalled = true
		if chatID != "console" {
			t.Errorf("expected chatID 'console', got %q", chatID)
		}
		return 5, nil
	}

	m := newModel(context.Background(), nil, "admin-1", "console", "test-inst", nil, compactFn, "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(tuiModel)

	// /compact is async: Enter returns a tea.Cmd.
	m.textarea.SetValue("/compact")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)

	if !m.streaming {
		t.Error("should be in streaming state while compact runs")
	}

	// Execute the async command.
	if cmd == nil {
		t.Fatal("expected async command for /compact")
	}
	result := cmd()
	if !compactCalled {
		t.Error("compact function should have been called")
	}

	// Feed result back.
	updated, _ = m.Update(result)
	m = updated.(tuiModel)

	if m.streaming {
		t.Error("should not be streaming after compact completes")
	}

	var found bool
	for _, msg := range m.messages {
		if msg.role == "status" && strings.Contains(msg.content, "5 messages") {
			found = true
		}
	}
	if !found {
		t.Error("expected compact success message")
	}
}

func TestIntegration_CompactCommand_NoFunc(t *testing.T) {
	m := newModel(context.Background(), nil, "admin-1", "console", "test-inst", nil, nil, "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(tuiModel)

	m.textarea.SetValue("/compact")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)

	var found bool
	for _, msg := range m.messages {
		if msg.role == "status" && strings.Contains(msg.content, "not available") {
			found = true
		}
	}
	if !found {
		t.Error("expected 'not available' error when compact func is nil")
	}
}

func TestIntegration_CompactCommand_Error(t *testing.T) {
	compactFn := func(_ context.Context, _ string) (int, error) {
		return 0, fmt.Errorf("not enough messages")
	}

	m := newModel(context.Background(), nil, "admin-1", "console", "test-inst", nil, compactFn, "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(tuiModel)

	// /compact is async.
	m.textarea.SetValue("/compact")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)

	// Execute and feed result back.
	if cmd != nil {
		result := cmd()
		updated, _ = m.Update(result)
		m = updated.(tuiModel)
	}

	var found bool
	for _, msg := range m.messages {
		if msg.role == "status" && strings.Contains(msg.content, "not enough messages") {
			found = true
		}
	}
	if !found {
		t.Error("expected error message from compact")
	}
}

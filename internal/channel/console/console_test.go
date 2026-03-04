package console

import (
	"context"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/i18n"
)

func TestMain(m *testing.M) {
	_ = i18n.Init()
	os.Exit(m.Run())
}

// --- Interface compliance ---

func TestChannel_ImplementsInputChannel(t *testing.T) {
	var _ channel.InputChannel = (*Channel)(nil)
}

func TestChannel_ImplementsStreamingSender(t *testing.T) {
	var _ channel.StreamingSender = (*Channel)(nil)
}

func TestChannel_ImplementsStatusNotifier(t *testing.T) {
	var _ channel.StatusNotifier = (*Channel)(nil)
}

// --- Model unit tests ---

func TestModel_Init(t *testing.T) {
	m := newModel(context.Background(), nil, "user-1", "console", "test", nil, nil, "", nil)
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init should return a batch command")
	}
}

func TestModel_WindowSizeMsg(t *testing.T) {
	m := newModel(context.Background(), nil, "user-1", "console", "test", nil, nil, "", nil)

	if m.ready {
		t.Fatal("model should not be ready before WindowSizeMsg")
	}

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m2 := updated.(tuiModel)

	if !m2.ready {
		t.Fatal("model should be ready after WindowSizeMsg")
	}
	if m2.width != 80 {
		t.Errorf("expected width 80, got %d", m2.width)
	}
	if m2.height != 24 {
		t.Errorf("expected height 24, got %d", m2.height)
	}
}

func TestModel_ViewBeforeReady(t *testing.T) {
	m := newModel(context.Background(), nil, "user-1", "console", "test", nil, nil, "", nil)
	view := m.View()
	if view != "Initializing..." {
		t.Errorf("expected initializing message, got: %q", view)
	}
}

func TestModel_ViewAfterReady(t *testing.T) {
	m := newModel(context.Background(), nil, "user-1", "console", "test", nil, nil, "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m2 := updated.(tuiModel)

	view := m2.View()
	if view == "Initializing..." {
		t.Error("should not show initializing after WindowSizeMsg")
	}
	// Should contain welcome text and status bar.
	if !strings.Contains(view, "iulita") {
		t.Error("view should contain 'iulita'")
	}
}

func TestModel_CtrlC(t *testing.T) {
	m := newModel(context.Background(), nil, "user-1", "console", "test", nil, nil, "", nil)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c should return a command")
	}
	// tea.Quit returns a function that returns tea.QuitMsg.
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("expected QuitMsg, got %T", msg)
	}
}

func TestModel_SlashQuit(t *testing.T) {
	m := newModel(context.Background(), nil, "user-1", "console", "test", nil, nil, "", nil)
	// Simulate ready state.
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m2 := updated.(tuiModel)

	// Type "/quit" into textarea.
	m2.textarea.SetValue("/quit")

	// Press Enter.
	updated, cmd := m2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	_ = updated
	if cmd == nil {
		t.Fatal("/quit should return a quit command")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("expected QuitMsg, got %T", msg)
	}
}

func TestModel_SlashExit(t *testing.T) {
	m := newModel(context.Background(), nil, "user-1", "console", "test", nil, nil, "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m2 := updated.(tuiModel)

	m2.textarea.SetValue("/exit")
	_, cmd := m2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("/exit should return a quit command")
	}
}

func TestModel_SlashHelp(t *testing.T) {
	m := newModel(context.Background(), nil, "user-1", "console", "test", nil, nil, "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m2 := updated.(tuiModel)

	m2.textarea.SetValue("/help")
	updated, _ = m2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m3 := updated.(tuiModel)

	if len(m3.messages) == 0 {
		t.Fatal("expected help message")
	}
	if m3.messages[len(m3.messages)-1].role != "status" {
		t.Error("help should be a status message")
	}
}

func TestModel_SlashClear(t *testing.T) {
	m := newModel(context.Background(), nil, "user-1", "console", "test", nil, nil, "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m2 := updated.(tuiModel)

	// Add some messages.
	m2.appendMessage("user", "hello")
	m2.appendMessage("assistant", "hi there")

	m2.textarea.SetValue("/clear")
	updated, _ = m2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m3 := updated.(tuiModel)

	if len(m3.messages) != 0 {
		t.Errorf("expected 0 messages after /clear, got %d", len(m3.messages))
	}
}

func TestModel_EmptyInput(t *testing.T) {
	m := newModel(context.Background(), nil, "user-1", "console", "test", nil, nil, "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m2 := updated.(tuiModel)

	m2.textarea.SetValue("")
	updated, _ = m2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m3 := updated.(tuiModel)

	if len(m3.messages) != 0 {
		t.Error("empty input should not create messages")
	}
}

func TestModel_UserMessage(t *testing.T) {
	handlerCalled := false
	handler := func(_ context.Context, msg channel.IncomingMessage) (string, error) {
		handlerCalled = true
		if msg.Text != "hello world" {
			t.Errorf("expected 'hello world', got %q", msg.Text)
		}
		if msg.ChatID != "console" {
			t.Errorf("expected chatID 'console', got %q", msg.ChatID)
		}
		if msg.ResolvedUserID != "user-1" {
			t.Errorf("expected userID 'user-1', got %q", msg.ResolvedUserID)
		}
		return "response text", nil
	}

	m := newModel(context.Background(), handler, "user-1", "console", "test", nil, nil, "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m2 := updated.(tuiModel)

	m2.textarea.SetValue("hello world")
	updated, cmd := m2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m3 := updated.(tuiModel)

	// User message should be appended.
	if len(m3.messages) == 0 || m3.messages[0].role != "user" || m3.messages[0].content != "hello world" {
		t.Error("expected user message")
	}

	// Should be streaming.
	if !m3.streaming {
		t.Error("should be in streaming state")
	}

	// Textarea should be cleared.
	if m3.textarea.Value() != "" {
		t.Error("textarea should be cleared after send")
	}

	// Execute the command (calls handler).
	if cmd != nil {
		msg := cmd()
		resp, ok := msg.(responseMsg)
		if !ok {
			t.Fatalf("expected responseMsg, got %T", msg)
		}
		if resp.text != "response text" {
			t.Errorf("expected 'response text', got %q", resp.text)
		}
	}

	if !handlerCalled {
		t.Error("handler should have been called")
	}
}

func TestModel_StreamChunk(t *testing.T) {
	m := newModel(context.Background(), nil, "user-1", "console", "test", nil, nil, "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m2 := updated.(tuiModel)

	updated, _ = m2.Update(streamChunkMsg("partial response"))
	m3 := updated.(tuiModel)

	if !m3.streaming {
		t.Error("should be streaming")
	}
	if m3.streamBuf != "partial response" {
		t.Errorf("expected stream buffer 'partial response', got %q", m3.streamBuf)
	}
}

func TestModel_StreamDone(t *testing.T) {
	m := newModel(context.Background(), nil, "user-1", "console", "test", nil, nil, "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m2 := updated.(tuiModel)

	// Start streaming.
	m2.streaming = true
	m2.streamBuf = "partial"

	// Finish streaming.
	updated, _ = m2.Update(streamDoneMsg("final response"))
	m3 := updated.(tuiModel)

	if m3.streaming {
		t.Error("should not be streaming after done")
	}
	if m3.streamBuf != "" {
		t.Error("stream buffer should be cleared")
	}
	if len(m3.messages) == 0 || m3.messages[len(m3.messages)-1].content != "final response" {
		t.Error("expected final response in messages")
	}
}

func TestModel_StatusMsg(t *testing.T) {
	m := newModel(context.Background(), nil, "user-1", "console", "test", nil, nil, "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m2 := updated.(tuiModel)

	// Skill start.
	updated, _ = m2.Update(statusMsg(channel.StatusEvent{Type: "skill_start", SkillName: "remember"}))
	m3 := updated.(tuiModel)

	if len(m3.messages) == 0 {
		t.Fatal("expected status message")
	}
	last := m3.messages[len(m3.messages)-1]
	if last.role != "status" {
		t.Errorf("expected status role, got %q", last.role)
	}
	if !strings.Contains(last.content, "remember") {
		t.Errorf("expected 'remember' in status, got %q", last.content)
	}
}

func TestModel_StatusMsg_Processing(t *testing.T) {
	m := newModel(context.Background(), nil, "user-1", "console", "test", nil, nil, "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m2 := updated.(tuiModel)

	updated, _ = m2.Update(statusMsg(channel.StatusEvent{Type: "processing"}))
	m3 := updated.(tuiModel)

	if len(m3.messages) == 0 {
		t.Fatal("expected thinking status")
	}
	if !strings.Contains(m3.messages[len(m3.messages)-1].content, "thinking") {
		t.Error("expected thinking badge")
	}
}

func TestModel_ProactiveMsg(t *testing.T) {
	m := newModel(context.Background(), nil, "user-1", "console", "test", nil, nil, "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m2 := updated.(tuiModel)

	updated, _ = m2.Update(proactiveMsg("You have a reminder!"))
	m3 := updated.(tuiModel)

	if len(m3.messages) == 0 {
		t.Fatal("expected proactive message")
	}
	last := m3.messages[len(m3.messages)-1]
	if last.role != "assistant" || last.content != "You have a reminder!" {
		t.Errorf("unexpected message: %+v", last)
	}
}

func TestModel_ResponseMsg(t *testing.T) {
	m := newModel(context.Background(), nil, "user-1", "console", "test", nil, nil, "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m2 := updated.(tuiModel)
	m2.streaming = true

	updated, _ = m2.Update(responseMsg{text: "hello from assistant"})
	m3 := updated.(tuiModel)

	if m3.streaming {
		t.Error("should stop streaming after response")
	}
	if len(m3.messages) == 0 || m3.messages[len(m3.messages)-1].content != "hello from assistant" {
		t.Error("expected response message")
	}
}

func TestModel_ResponseMsg_Error(t *testing.T) {
	m := newModel(context.Background(), nil, "user-1", "console", "test", nil, nil, "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m2 := updated.(tuiModel)
	m2.streaming = true

	updated, _ = m2.Update(responseMsg{err: context.DeadlineExceeded})
	m3 := updated.(tuiModel)

	if len(m3.messages) == 0 {
		t.Fatal("expected error message")
	}
	last := m3.messages[len(m3.messages)-1]
	if last.role != "error" {
		t.Errorf("expected error role, got %q", last.role)
	}
}

func TestModel_WindowResize(t *testing.T) {
	m := newModel(context.Background(), nil, "user-1", "console", "test", nil, nil, "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m2 := updated.(tuiModel)

	// Resize.
	updated, _ = m2.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m3 := updated.(tuiModel)

	if m3.width != 120 {
		t.Errorf("expected width 120, got %d", m3.width)
	}
	if m3.height != 40 {
		t.Errorf("expected height 40, got %d", m3.height)
	}
}

func TestModel_MarkdownRendering(t *testing.T) {
	m := newModel(context.Background(), nil, "user-1", "console", "test", nil, nil, "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m2 := updated.(tuiModel)

	// Add a markdown message.
	m2.appendMessage("assistant", "**bold** text")
	m2.refreshViewport()

	view := m2.View()
	// The view should contain the rendered content (exact rendering depends on glamour).
	if !strings.Contains(view, "bold") {
		t.Errorf("expected 'bold' in rendered view, got: %s", view)
	}
}

func TestModel_MsgCount(t *testing.T) {
	m := newModel(context.Background(), nil, "user-1", "console", "test", nil, nil, "", nil)

	m.appendMessage("user", "hello")
	m.appendMessage("assistant", "hi")
	m.appendMessage("status", "thinking")
	m.appendMessage("user", "bye")

	// Only user + assistant count.
	if m.msgCount != 3 {
		t.Errorf("expected 3 msg count, got %d", m.msgCount)
	}
}

func TestModel_InputBlockedWhileStreaming(t *testing.T) {
	m := newModel(context.Background(), nil, "user-1", "console", "test", nil, nil, "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m2 := updated.(tuiModel)
	m2.streaming = true

	// Try to send while streaming.
	m2.textarea.SetValue("should not send")
	updated, cmd := m2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m3 := updated.(tuiModel)

	// Enter should be ignored while streaming.
	if len(m3.messages) > 0 {
		t.Error("should not send message while streaming")
	}
	_ = cmd
}

// --- Channel method tests ---

func TestStartStream_NotRunning(t *testing.T) {
	ch := New(nil)
	_, _, err := ch.StartStream(context.Background(), "console", 0)
	if err == nil {
		t.Error("expected error when program not running")
	}
}

func TestSendMessage_NotRunning(t *testing.T) {
	ch := New(nil)
	err := ch.SendMessage(context.Background(), "console", "test")
	if err != nil {
		t.Error("SendMessage should not error when program not running")
	}
}

func TestNotifyStatus_NotRunning(t *testing.T) {
	ch := New(nil)
	err := ch.NotifyStatus(context.Background(), "console", channel.StatusEvent{Type: "processing"})
	if err != nil {
		t.Error("NotifyStatus should not error when program not running")
	}
}

// --- Slash command tests ---

func TestSlashCommands_Help(t *testing.T) {
	m := newModel(context.Background(), nil, "user-1", "console", "test", nil, nil, "", nil)
	handled, output := trySlashCommand(&m, "/help")
	if !handled {
		t.Error("/help should be handled")
	}
	if !strings.Contains(output, "/quit") {
		t.Error("help output should contain /quit")
	}
}

func TestSlashCommands_Clear(t *testing.T) {
	m := newModel(context.Background(), nil, "user-1", "console", "test", nil, nil, "", nil)
	m.appendMessage("user", "test")
	handled, _ := trySlashCommand(&m, "/clear")
	if !handled {
		t.Error("/clear should be handled")
	}
	if len(m.messages) != 0 {
		t.Error("messages should be cleared")
	}
}

func TestSlashCommands_Unknown(t *testing.T) {
	m := newModel(context.Background(), nil, "user-1", "console", "test", nil, nil, "", nil)
	handled, _ := trySlashCommand(&m, "/unknown_command")
	if handled {
		t.Error("unknown command should not be handled locally")
	}
}

func TestSlashCommands_CaseInsensitive(t *testing.T) {
	m := newModel(context.Background(), nil, "user-1", "console", "test", nil, nil, "", nil)
	handled, _ := trySlashCommand(&m, "/HELP")
	if !handled {
		t.Error("/HELP should be handled (case insensitive)")
	}
}

func TestSlashCommands_Status(t *testing.T) {
	m := newModel(context.Background(), nil, "user-1", "console", "test", nil, nil, "", nil)
	handled, output := trySlashCommand(&m, "/status")
	if !handled {
		t.Error("/status should be handled")
	}
	if !strings.Contains(output, "not available") {
		t.Errorf("expected 'not available' without provider, got: %s", output)
	}
}

func TestSlashCommands_Compact(t *testing.T) {
	m := newModel(context.Background(), nil, "user-1", "console", "test", nil, nil, "", nil)
	handled, output := trySlashCommand(&m, "/compact")
	if !handled {
		t.Error("/compact should be handled")
	}
	// /compact is handled async in Update, so trySlashCommand returns empty.
	if output != "" {
		t.Errorf("expected empty output from trySlashCommand for /compact, got: %s", output)
	}
}

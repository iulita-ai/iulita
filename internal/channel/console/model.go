package console

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/i18n"
	"github.com/iulita-ai/iulita/internal/version"
)

const (
	textareaHeight = 3
	statusBarLines = 1
	dividerLines   = 1
	overhead       = textareaHeight + statusBarLines + dividerLines + 2 // +2 buffer for alt screen
)

// chatMessage holds a single message in the chat history.
type chatMessage struct {
	role    string // "user", "assistant", "status", "error"
	content string // raw content
}

// tuiModel is the bubbletea model for the console chat.
type tuiModel struct {
	// Layout components
	viewport viewport.Model
	textarea textarea.Model
	spinner  spinner.Model

	// Dimensions
	width  int
	height int
	ready  bool

	// Chat state
	messages      []chatMessage
	streaming     bool
	streamBuf     string // current streaming response (full text)
	streamDoneRcv bool   // true if streamDoneMsg was received for current turn

	// Channel integration
	handler        channel.MessageHandler
	ctx            context.Context
	userID         string
	chatID         string
	instanceID     string
	statusProvider *StatusProvider
	compactFn      CompactFunc

	// Rendering
	mdRenderer   *glamour.TermRenderer
	glamourStyle string // "dark" or "light", detected before TUI starts
	styles       uiStyles

	// Interactive prompts
	promptActive bool     // true when waiting for prompt response
	consoleCh    *Channel // back-reference for prompt resolution

	// Message count for status bar
	msgCount int
}

func newModel(ctx context.Context, handler channel.MessageHandler, userID, chatID, instanceID string, sp *StatusProvider, compactFn CompactFunc, glamourStyle string, consoleCh *Channel) tuiModel {
	ta := textarea.New()
	ta.Placeholder = i18n.T(ctx, "ConsolePlaceholder")
	ta.ShowLineNumbers = false
	ta.SetHeight(textareaHeight)
	ta.Focus()
	ta.CharLimit = 4096

	// Customize textarea key map: Enter sends, Alt+Enter for newline.
	ta.KeyMap.InsertNewline.SetKeys("alt+enter")

	spin := spinner.New()
	spin.Spinner = spinner.Dot

	if glamourStyle == "" {
		glamourStyle = "dark"
	}

	renderer, _ := glamour.NewTermRenderer(
		glamour.WithStylePath(glamourStyle),
		glamour.WithWordWrap(70),
	)

	return tuiModel{
		textarea:       ta,
		spinner:        spin,
		handler:        handler,
		ctx:            ctx,
		userID:         userID,
		chatID:         chatID,
		instanceID:     instanceID,
		statusProvider: sp,
		compactFn:      compactFn,
		mdRenderer:     renderer,
		styles:         defaultStyles(),
		glamourStyle:   glamourStyle,
		consoleCh:      consoleCh,
	}
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(
		textarea.Blink,
		m.spinner.Tick,
	)
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		vpHeight := m.height - overhead
		if vpHeight < 1 {
			vpHeight = 1
		}

		if !m.ready {
			m.viewport = viewport.New(m.width, vpHeight)
			m.viewport.SetContent(m.renderWelcome())
			m.ready = true
		} else {
			m.viewport.Width = m.width
			m.viewport.Height = vpHeight
		}

		m.textarea.SetWidth(m.width)

		// Re-render with new width.
		m.refreshMarkdownRenderer()
		m.refreshViewport()

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit

		case "enter":
			if m.streaming && !m.promptActive {
				break // ignore input while streaming (but allow during prompts)
			}
			input := strings.TrimSpace(m.textarea.Value())
			if input == "" {
				break
			}
			m.textarea.Reset()

			// Route to pending interactive prompt if active.
			if m.promptActive && m.consoleCh != nil {
				if result, consumed := m.consoleCh.ResolvePromptInput(input); consumed {
					m.appendMessage("user", input)
					if result == "__other__" {
						// "Enter manually" selected — stay in prompt mode.
						m.appendMessage("status", m.styles.skillBadge.Render(i18n.T(m.ctx, "ConsoleTypeAnswer")))
					} else {
						m.promptActive = false
						m.streaming = false
					}
					m.refreshViewport()
					break
				}
			}

			// Check for /quit, /exit.
			lower := strings.ToLower(input)
			if lower == "/quit" || lower == "/exit" {
				return m, tea.Quit
			}

			// /compact is handled async to avoid blocking the TUI.
			if lower == "/compact" {
				if m.compactFn == nil {
					m.appendMessage("status", m.styles.errorLine.Render(i18n.T(m.ctx, "ConsoleCompactUnavailable")))
					m.refreshViewport()
				} else {
					m.appendMessage("status", m.styles.thinkBadge.Render(i18n.T(m.ctx, "ConsoleCompressing")))
					m.refreshViewport()
					m.streaming = true
					cmds = append(cmds, m.runCompact())
				}
				break
			}

			// Check for local slash commands.
			if handled, output := trySlashCommand(&m, input); handled {
				if output != "" {
					m.appendMessage("status", output)
					m.refreshViewport()
				}
				break
			}

			// Send to assistant.
			m.appendMessage("user", input)
			m.refreshViewport()
			m.streaming = true
			cmds = append(cmds, m.sendToAssistant(input))
		}

	case sendMsg:
		// Not used directly — kept for future expansion.

	case responseMsg:
		m.streaming = false
		if msg.err != nil {
			m.appendMessage("error", i18n.T(m.ctx, "ConsoleErrorPrefix", map[string]any{"Error": fmt.Sprintf("%v", msg.err)}))
		} else if msg.text != "" && !m.streamDoneRcv {
			// Only append text if streamDoneMsg didn't already deliver it.
			m.appendMessage("assistant", msg.text)
		}
		m.streamDoneRcv = false
		m.refreshViewport()

	case streamChunkMsg:
		m.streaming = true
		m.streamBuf = string(msg)
		m.refreshViewport()
		cmds = append(cmds, m.spinner.Tick)

	case streamDoneMsg:
		m.streaming = false
		m.streamDoneRcv = true
		finalText := string(msg)
		if finalText == "" {
			finalText = m.streamBuf
		}
		m.streamBuf = ""
		if finalText != "" {
			m.appendMessage("assistant", finalText)
		}
		m.refreshViewport()

	case statusMsg:
		evt := channel.StatusEvent(msg)
		var line string
		switch evt.Type {
		case "processing":
			line = m.styles.thinkBadge.Render(i18n.T(m.ctx, "ConsoleThinking"))
		case "skill_start":
			line = m.styles.skillBadge.Render(i18n.T(m.ctx, "ConsoleSkillRunning", map[string]any{"Name": evt.SkillName}))
		case "skill_done":
			if !evt.Success {
				line = m.styles.errorLine.Render(i18n.T(m.ctx, "ConsoleSkillFailed", map[string]any{"Name": evt.SkillName}))
			}
			// Don't print success — response follows.
		case "error":
			line = m.styles.errorLine.Render(i18n.T(m.ctx, "ConsoleErrorPrefix", map[string]any{"Error": evt.Error}))
		}
		if line != "" {
			m.appendMessage("status", line)
			m.refreshViewport()
		}

	case promptMsg:
		// Display interactive prompt with numbered options.
		var b strings.Builder
		fmt.Fprintf(&b, "%s\n", msg.question)
		for i, opt := range msg.options {
			fmt.Fprintf(&b, "  %d. %s\n", i+1, opt.Label)
		}
		fmt.Fprintf(&b, "  %d. %s\n", len(msg.options)+1, i18n.T(m.ctx, "ConsoleEnterManually"))
		b.WriteString(i18n.T(m.ctx, "ConsoleTypeNumberOrAnswer"))
		m.appendMessage("status", m.styles.skillBadge.Render(b.String()))
		m.promptActive = true
		m.refreshViewport()

	case proactiveMsg:
		m.appendMessage("assistant", string(msg))
		m.refreshViewport()

	case compactResultMsg:
		m.streaming = false
		if msg.err != nil {
			m.appendMessage("status", m.styles.errorLine.Render(i18n.T(m.ctx, "ConsoleCompressFailed", map[string]any{"Error": fmt.Sprintf("%v", msg.err)})))
		} else {
			m.appendMessage("status", m.styles.statusLine.Render(i18n.T(m.ctx, "ConsoleCompressDone", map[string]any{"Count": msg.removed})))
		}
		m.refreshViewport()

	case spinner.TickMsg:
		if m.streaming {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			cmds = append(cmds, cmd)
		}
	}

	// Update textarea (handles key input). Allow input during prompts.
	if !m.streaming || m.promptActive {
		var cmd tea.Cmd
		m.textarea, cmd = m.textarea.Update(msg)
		cmds = append(cmds, cmd)
	}

	// Update viewport — only pass through non-conflicting messages.
	// Arrow keys belong to textarea; viewport gets pgup/pgdn/home/end
	// and non-key messages (window resize, mouse, etc.).
	passToViewport := true
	if keyMsg, ok := msg.(tea.KeyMsg); ok && !m.streaming {
		switch keyMsg.String() {
		case "up", "down", "left", "right":
			passToViewport = false
		}
	}
	if passToViewport {
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

func (m tuiModel) View() string {
	if !m.ready {
		return "Initializing..."
	}

	// Chat area — strip trailing whitespace padding added by the viewport
	// component. The viewport pads every line to viewport.Width using
	// lipgloss.Width measurement. If content has emoji that lipgloss
	// undercounts, padding makes lines wider than the terminal → wrapping
	// → ghost UI elements. Stripping padding keeps lines at natural width.
	chatArea := stripTrailingSpaces(m.viewport.View())

	// Divider.
	divider := m.styles.divider.Render(strings.Repeat("─", m.width))

	// Input area.
	inputArea := m.textarea.View()

	// Status bar.
	statusBar := m.renderStatusBar()

	// Join manually with "\n" instead of lipgloss.JoinVertical.
	// JoinVertical pads ALL lines to the widest block's width,
	// re-introducing the same padding problem we stripped above.
	return chatArea + "\n" + divider + "\n" + inputArea + "\n" + statusBar
}

// sendToAssistant dispatches the user message to the assistant handler in a goroutine.
// The handler may also send streamChunkMsg/streamDoneMsg via StartStream concurrently.
func (m *tuiModel) sendToAssistant(text string) tea.Cmd {
	handler := m.handler
	ctx := m.ctx
	chatID := m.chatID
	userID := m.userID
	instanceID := m.instanceID

	return func() tea.Msg {
		msg := channel.IncomingMessage{
			ChatID:            chatID,
			UserID:            userID,
			ResolvedUserID:    userID,
			ChannelInstanceID: instanceID,
			Text:              text,
			Caps:              channel.CapTyping,
		}
		resp, err := handler(ctx, msg)
		return responseMsg{text: resp, err: err}
	}
}

// runCompact dispatches the compact operation in a goroutine to avoid blocking the TUI.
func (m *tuiModel) runCompact() tea.Cmd {
	compactFn := m.compactFn
	ctx := m.ctx
	chatID := m.chatID
	return func() tea.Msg {
		removed, err := compactFn(ctx, chatID)
		return compactResultMsg{removed: removed, err: err}
	}
}

// appendMessage adds a message to the chat history.
func (m *tuiModel) appendMessage(role, content string) {
	m.messages = append(m.messages, chatMessage{role: role, content: content})
	if role == "user" || role == "assistant" {
		m.msgCount++
	}
}

// refreshViewport re-renders all messages and updates the viewport content.
func (m *tuiModel) refreshViewport() {
	var b strings.Builder

	for _, msg := range m.messages {
		m.renderMessage(&b, msg)
	}

	// Show streaming buffer if active.
	if m.streaming && m.streamBuf != "" {
		b.WriteString("\n")
		b.WriteString(m.styles.botLabel.Render(i18n.T(m.ctx, "ConsoleBotLabel")))
		b.WriteString("\n")
		rendered := m.renderMarkdown(m.streamBuf)
		b.WriteString(rendered)
		b.WriteString(m.spinner.View())
	} else if m.streaming {
		b.WriteString("\n")
		b.WriteString(m.spinner.View())
		b.WriteString(" ")
		b.WriteString(m.styles.thinkBadge.Render(i18n.T(m.ctx, "ConsoleProcessing")))
	}

	m.viewport.SetContent(b.String())
	m.viewport.GotoBottom()
}

// renderMessage formats a single chat message.
// All output is constrained to m.width to prevent terminal-level line wrapping
// which would desync the viewport's logical line count with visual lines.
func (m *tuiModel) renderMessage(b *strings.Builder, msg chatMessage) {
	if b.Len() > 0 {
		b.WriteString("\n")
	}

	maxW := m.width
	if maxW < 20 {
		maxW = 20
	}

	switch msg.role {
	case "user":
		b.WriteString(m.styles.userLabel.Render(i18n.T(m.ctx, "ConsoleUserLabel")))
		b.WriteString("\n")
		wrapped := lipgloss.NewStyle().Width(maxW - 2).Render(msg.content)
		b.WriteString(m.styles.userMsg.Render(wrapped))

	case "assistant":
		b.WriteString(m.styles.botLabel.Render(i18n.T(m.ctx, "ConsoleBotLabel")))
		b.WriteString("\n")
		rendered := m.renderMarkdown(msg.content)
		b.WriteString(rendered)

	case "status":
		b.WriteString(lipgloss.NewStyle().MaxWidth(maxW).Render(msg.content))

	case "error":
		b.WriteString(lipgloss.NewStyle().MaxWidth(maxW).Render(
			m.styles.errorLine.Render(msg.content)))
	}
}

// renderMarkdown renders markdown text using glamour.
func (m *tuiModel) renderMarkdown(text string) string {
	if m.mdRenderer == nil {
		return text
	}
	rendered, err := m.mdRenderer.Render(text)
	if err != nil {
		return text
	}
	return strings.TrimSpace(rendered)
}

// refreshMarkdownRenderer recreates the renderer with current width.
func (m *tuiModel) refreshMarkdownRenderer() {
	// Conservative word wrap: leave generous margin for emoji characters
	// that terminals render wider than lipgloss/runewidth measure.
	// Glamour adds ~2 cells of left padding, so effective content width
	// is wordWrap + 2. After stripping viewport padding, lines stay at
	// this natural width — well under m.width even with emoji overhead.
	wordWrap := m.width - 16
	if wordWrap > 120 {
		wordWrap = 120 // cap for very wide terminals
	}
	if wordWrap < 40 {
		wordWrap = 40
	}
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStylePath(m.glamourStyle),
		glamour.WithWordWrap(wordWrap),
	)
	if err == nil {
		m.mdRenderer = renderer
	}
}

// stripTrailingSpaces removes trailing ASCII spaces from each line.
// This undoes the viewport/lipgloss padding that can push emoji-heavy
// lines past terminal width.
func stripTrailingSpaces(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	return strings.Join(lines, "\n")
}

// renderWelcome returns the welcome text shown on startup.
func (m *tuiModel) renderWelcome() string {
	var b strings.Builder
	b.WriteString(m.styles.botLabel.Render("iulita"))
	b.WriteString(" ")
	b.WriteString(m.styles.statusLine.Render(version.String()))
	b.WriteString("\n")
	b.WriteString(m.styles.statusLine.Render(i18n.T(m.ctx, "ConsoleWelcomeHint")))
	b.WriteString("\n")
	return b.String()
}

// renderStatusBar renders the bottom status bar.
func (m *tuiModel) renderStatusBar() string {
	ver := "iulita " + version.Short()
	user := m.userID
	if len(user) > 8 {
		user = user[:8] + "..."
	}
	msgs := i18n.T(m.ctx, "ConsoleMsgCount", map[string]any{"Count": m.msgCount})

	left := fmt.Sprintf(" %s | %s | %s", ver, user, msgs)

	right := i18n.T(m.ctx, "ConsoleStatusBarRight")
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 0 {
		gap = 0
	}

	bar := left + strings.Repeat(" ", gap) + right
	return m.styles.statusBar.MaxWidth(m.width).Render(bar)
}

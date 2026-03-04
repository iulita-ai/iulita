package console

import (
	"context"
	"fmt"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/channel"
)

// StatusProvider supplies system status info for the /status command.
type StatusProvider struct {
	EnabledSkills func() int
	TotalSkills   func() int
	DailyCost     func() float64
	SessionStats  func() (inputTokens, outputTokens, requests int64)
}

// CompactFunc triggers manual context compression for a given chatID.
// Returns the number of messages compressed and an error if any.
type CompactFunc func(ctx context.Context, chatID string) (int, error)

// Channel implements a full-screen TUI chat using bubbletea.
// It satisfies InputChannel, StreamingSender, and StatusNotifier.
type Channel struct {
	instanceID string
	userID     string // pre-resolved admin user
	chatID     string // fixed "console"
	logger     *zap.Logger

	statusProvider *StatusProvider
	compactFn      CompactFunc
	onExit         func() // called when TUI exits, before Start() returns

	mu      sync.RWMutex
	program *tea.Program

	promptMu      sync.Mutex
	pendingPrompt *pendingConsolePrompt
}

// New creates a console channel.
func New(logger *zap.Logger) *Channel {
	return &Channel{
		chatID: "console",
		logger: logger,
	}
}

func (c *Channel) SetInstanceID(id string)              { c.instanceID = id }
func (c *Channel) SetUserID(userID string)              { c.userID = userID }
func (c *Channel) SetStatusProvider(sp *StatusProvider) { c.statusProvider = sp }
func (c *Channel) SetCompactFunc(fn CompactFunc)        { c.compactFn = fn }
func (c *Channel) SetOnExit(fn func())                  { c.onExit = fn }

// Start implements InputChannel. It launches the bubbletea TUI and blocks until exit.
func (c *Channel) Start(ctx context.Context, handler channel.MessageHandler) error {
	if c.userID == "" {
		c.userID = "console"
	}

	// Detect terminal background BEFORE starting bubbletea so the escape
	// sequence response doesn't leak into the textarea as text.
	glamourStyle := "dark"
	if !lipgloss.HasDarkBackground() {
		glamourStyle = "light"
	}

	model := newModel(ctx, handler, c.userID, c.chatID, c.instanceID, c.statusProvider, c.compactFn, glamourStyle, c)

	p := tea.NewProgram(
		model,
		tea.WithAltScreen(),
	)

	c.mu.Lock()
	c.program = p
	c.mu.Unlock()

	_, err := p.Run()

	// Notify that the TUI has exited (triggers app shutdown).
	if c.onExit != nil {
		c.onExit()
	}

	if err != nil {
		return fmt.Errorf("console TUI error: %w", err)
	}
	return nil
}

// getProgram returns the tea.Program in a thread-safe manner.
func (c *Channel) getProgram() *tea.Program {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.program
}

// StartStream implements StreamingSender. Returns functions to update and finalize
// a streaming response in the TUI.
func (c *Channel) StartStream(_ context.Context, _ string, _ int) (editFn func(string), doneFn func(string), err error) {
	p := c.getProgram()
	if p == nil {
		return nil, nil, fmt.Errorf("console program not running")
	}

	editFn = func(text string) {
		p.Send(streamChunkMsg(text))
	}
	doneFn = func(text string) {
		p.Send(streamDoneMsg(text))
	}
	return editFn, doneFn, nil
}

// SendMessage implements MessageSender for proactive messages.
func (c *Channel) SendMessage(_ context.Context, _ string, text string) error {
	p := c.getProgram()
	if p == nil {
		return nil
	}
	p.Send(proactiveMsg(text))
	return nil
}

// NotifyStatus implements StatusNotifier for processing status events.
func (c *Channel) NotifyStatus(_ context.Context, _ string, event channel.StatusEvent) error {
	p := c.getProgram()
	if p == nil {
		return nil
	}
	p.Send(statusMsg(event))
	return nil
}

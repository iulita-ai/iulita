package telegram

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/iulita-ai/iulita/internal/channel"
)

const (
	// editRateLimit is the minimum interval between status message edits.
	editRateLimit = 2 * time.Second
	// minStatusDisplay is the minimum time a status message must be visible
	// before being deleted, preventing invisible message flashes.
	minStatusDisplay = 500 * time.Millisecond
	// longTaskThreshold is the time after which the status message is kept
	// as a separate message instead of being replaced by the response.
	// For long tasks, users want to see the execution log alongside the response.
	longTaskThreshold = 30 * time.Second
)

// statusEntry tracks a live status message for a single chat.
type statusEntry struct {
	tgChatID     int64
	msgID        int
	replyTo      int // original user message ID for reply threading
	lines        []string
	lastEdit     time.Time
	sentAt       time.Time
	consumed     bool // true = streaming has taken ownership of the message
	skipBookmark bool // true = remember skill was used, skip bookmark button
	mu           sync.Mutex
}

// addLine appends a new status line.
func (e *statusEntry) addLine(line string) {
	e.mu.Lock()
	e.lines = append(e.lines, line)
	e.mu.Unlock()
}

// updateLastLine replaces the last line (e.g., skill_done replacing skill_start).
func (e *statusEntry) updateLastLine(line string) {
	e.mu.Lock()
	if len(e.lines) > 0 {
		e.lines[len(e.lines)-1] = line
	} else {
		e.lines = append(e.lines, line)
	}
	e.mu.Unlock()
}

// renderText builds the display string from all lines.
func (e *statusEntry) renderText() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return strings.Join(e.lines, "\n")
}

// canEdit returns true if enough time has passed since the last edit.
func (e *statusEntry) canEdit() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return time.Since(e.lastEdit) >= editRateLimit
}

// markEdited records that an edit was just performed.
func (e *statusEntry) markEdited() {
	e.mu.Lock()
	e.lastEdit = time.Now()
	e.mu.Unlock()
}

// markConsumed marks this status message as taken over by streaming.
// Returns the Telegram message ID so the caller can reuse it.
func (e *statusEntry) markConsumed() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.consumed = true
	return e.msgID
}

// isConsumed returns whether streaming has taken ownership.
func (e *statusEntry) isConsumed() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.consumed
}

// getMsgID returns the Telegram message ID.
func (e *statusEntry) getMsgID() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.msgID
}

// isLongTask returns true if the status message has been alive for longer
// than longTaskThreshold. For long tasks, the status message should be kept
// as a separate message rather than being replaced by the response.
func (e *statusEntry) isLongTask() bool {
	return time.Since(e.sentAt) > longTaskThreshold
}

// statusState is the per-Channel registry of live status messages.
type statusState struct {
	mu      sync.Mutex
	entries map[string]*statusEntry
}

func newStatusState() *statusState {
	return &statusState{
		entries: make(map[string]*statusEntry),
	}
}

// setReplyTo records the original message ID for reply threading.
// Called from processMsg before the handler runs.
func (ss *statusState) setReplyTo(chatID string, replyTo int) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	// Store as a placeholder entry — the actual entry is created later by NotifyStatus.
	if e, ok := ss.entries[chatID]; ok {
		e.replyTo = replyTo
	} else {
		// Pre-create a lightweight entry just to hold replyTo.
		ss.entries[chatID] = &statusEntry{replyTo: replyTo}
	}
}

func (ss *statusState) create(chatID string, tgChatID int64, msgID int) *statusEntry {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	// Preserve replyTo from pre-existing placeholder entry.
	var replyTo int
	if existing, ok := ss.entries[chatID]; ok {
		replyTo = existing.replyTo
	}
	e := &statusEntry{
		tgChatID: tgChatID,
		msgID:    msgID,
		replyTo:  replyTo,
		sentAt:   time.Now(),
		lastEdit: time.Now(), // count the initial send as an edit
	}
	ss.entries[chatID] = e
	return e
}

func (ss *statusState) get(chatID string) (*statusEntry, bool) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	e, ok := ss.entries[chatID]
	return e, ok
}

// getAndMarkConsumed atomically gets the entry and marks it as consumed.
// Returns nil if no entry exists. Prevents TOCTOU between get and markConsumed.
func (ss *statusState) getAndMarkConsumed(chatID string) *statusEntry {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	e, ok := ss.entries[chatID]
	if !ok {
		return nil
	}
	e.mu.Lock()
	e.consumed = true
	e.mu.Unlock()
	return e
}

func (ss *statusState) remove(chatID string) {
	ss.mu.Lock()
	delete(ss.entries, chatID)
	ss.mu.Unlock()
}

// skillEmoji returns an emoji for the given skill name.
func skillEmoji(name string) string {
	switch name {
	case "websearch":
		return "🔍"
	case "webfetch":
		return "📡"
	case "remember":
		return "💾"
	case "recall":
		return "🧠"
	case "forget":
		return "🗑"
	case "delegate", "orchestrate":
		return "🤖"
	case "tasks", "todoist":
		return "📋"
	case "reminders":
		return "⏰"
	case "weather":
		return "🌤"
	case "directives":
		return "📌"
	case "shell_exec":
		return "💻"
	case "pdf_read":
		return "📄"
	case "set_language":
		return "🌐"
	case "exchange_rate":
		return "💱"
	case "geolocation":
		return "📍"
	case "datetime":
		return "🕐"
	default:
		return "🔧"
	}
}

// formatStatusLine returns the text line for a status event,
// and whether it should replace the last line (true for skill_done).
func formatStatusLine(event channel.StatusEvent) (string, bool) {
	switch event.Type {
	case "processing":
		return "🔄 Processing...", false

	case "skill_start":
		emoji := skillEmoji(event.SkillName)
		return fmt.Sprintf("%s %s...", emoji, event.SkillName), false

	case "skill_done":
		emoji := skillEmoji(event.SkillName)
		if event.Success {
			return fmt.Sprintf("%s %s ✅ (%dms)", emoji, event.SkillName, event.DurationMs), true
		}
		return fmt.Sprintf("%s %s ❌", emoji, event.SkillName), true

	case "orchestration_started":
		count := "?"
		if event.Data != nil {
			if c, ok := event.Data["agent_count"]; ok {
				count = c
			}
		}
		return fmt.Sprintf("🤖 Orchestration (%s agents)", count), false

	case "agent_started":
		agentID := ""
		agentType := ""
		if event.Data != nil {
			agentID = event.Data["agent_id"]
			agentType = event.Data["agent_type"]
		}
		return fmt.Sprintf("  → %s (%s)...", agentID, agentType), false

	case "agent_completed":
		agentID := ""
		dur := ""
		if event.Data != nil {
			agentID = event.Data["agent_id"]
			dur = event.Data["duration_ms"]
		}
		return fmt.Sprintf("  → %s ✅ (%sms)", agentID, dur), false

	case "agent_failed":
		agentID := ""
		if event.Data != nil {
			agentID = event.Data["agent_id"]
		}
		return fmt.Sprintf("  → %s ❌", agentID), false

	case "orchestration_done":
		success := "?"
		if event.Data != nil {
			if s, ok := event.Data["success_count"]; ok {
				success = s
			}
		}
		return fmt.Sprintf("🤖 Orchestration done (%s succeeded)", success), false

	case "error":
		return fmt.Sprintf("❌ %s", event.Error), false

	case "skip_bookmark":
		return "", false // handled separately, no visible line

	default:
		return "", false
	}
}

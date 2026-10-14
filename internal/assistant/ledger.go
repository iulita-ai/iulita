package assistant

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/iulita-ai/iulita/internal/llm"
)

// ExplorationLedger tracks tool calls within a single HandleMessage run
// to prevent redundant queries.
type ExplorationLedger struct {
	entries []ledgerEntry
}

type ledgerEntry struct {
	ToolName string
	InputKey string // normalized input for dedup ("" for non-cacheable calls)
	Display  string // sanitized one-line input description for the summary
	Result   string
	IsError  bool
}

// NewExplorationLedger creates a fresh ledger for a request.
func NewExplorationLedger() *ExplorationLedger {
	return &ExplorationLedger{}
}

// Record stores a tool call and its result.
func (l *ExplorationLedger) Record(tc llm.ToolCall, result llm.ToolResult) {
	l.entries = append(l.entries, ledgerEntry{
		ToolName: tc.Name,
		InputKey: normalizeInput(tc.Input),
		Display:  truncate(normalizeInput(tc.Input), 80),
		Result:   result.Content,
		IsError:  result.IsError,
	})
}

// RecordNonCacheable stores an operation whose identical re-issue is a
// separate operation (HTTP requests, mutations): it is excluded from the
// dedup cache and rendered in the summary via the sanitized display line
// instead of the raw input.
func (l *ExplorationLedger) RecordNonCacheable(tc llm.ToolCall, result llm.ToolResult, display string) {
	if display == "" {
		display = tc.Name
	}
	l.entries = append(l.entries, ledgerEntry{
		ToolName: tc.Name,
		InputKey: "", // never matched by IsDuplicate
		Display:  truncate(display, 120),
		Result:   result.Content,
		IsError:  result.IsError,
	})
}

// IsDuplicate returns true if an identical tool call was already made.
func (l *ExplorationLedger) IsDuplicate(tc llm.ToolCall) (string, bool) {
	key := normalizeInput(tc.Input)
	if key == "" {
		return "", false
	}
	for _, e := range l.entries {
		if e.ToolName == tc.Name && e.InputKey == key {
			return e.Result, true
		}
	}
	return "", false
}

// Summary returns a brief description of tools called so far,
// suitable for injection into the system prompt.
func (l *ExplorationLedger) Summary() string {
	if len(l.entries) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("Tools already called in this turn:\n")
	for i, e := range l.entries {
		status := "ok"
		if e.IsError {
			status = "error"
		}
		fmt.Fprintf(&b, "%d. %s(%s) → %s\n", i+1, e.ToolName, e.Display, status)
	}
	return b.String()
}

func normalizeInput(input json.RawMessage) string {
	// Use canonical JSON form for comparison.
	var v any
	if err := json.Unmarshal(input, &v); err != nil {
		return string(input)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return string(input)
	}
	return string(b)
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

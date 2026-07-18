package importer

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
)

// ConversationResult is the mapped output for one archived conversation.
type ConversationResult struct {
	Conversation    domain.ImportedConversation
	Messages        []domain.ImportedMessage
	SkippedEmpty    int  // messages that reconstructed to empty content
	SkippedBinaries int  // binary files referenced but not present in the export
	ParseErrors     int  // non-empty timestamps that failed to parse
	Oversized       bool // raw element exceeded maxConvBytes; messages were not mapped
}

// StreamConversations decodes conversations.json (a JSON array) one element at a time
// without loading the whole ~226MB file into memory. Each element is handed to fn as
// raw JSON so the caller can size-guard and map it tolerantly. Peak memory is one
// conversation. A structural JSON error (a truly malformed array) is returned as a
// hard error; per-element mapping tolerance is the caller's responsibility.
func StreamConversations(r io.Reader, fn func(raw json.RawMessage) error) error {
	dec := json.NewDecoder(r)
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("reading conversations opening token: %w", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return fmt.Errorf("conversations.json: expected JSON array, got %v", tok)
	}
	for dec.More() {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return fmt.Errorf("decoding conversation element: %w", err)
		}
		if err := fn(raw); err != nil {
			return err
		}
	}
	return nil
}

// MapConversation maps one raw conversation element into an archive header plus its
// ordered messages. maxConvBytes>0 caps the raw element size: an oversized element is
// flagged (best-effort UUID captured) and its messages are not mapped, so a single
// pathological element cannot blow up memory. Returns an error only on JSON
// unmarshal failure, which the caller counts as a parse error and skips.
func MapConversation(raw []byte, userID string, maxConvBytes int) (ConversationResult, error) {
	var res ConversationResult

	if maxConvBytes > 0 && len(raw) > maxConvBytes {
		res.Oversized = true
		var hdr struct {
			UUID string `json:"uuid"`
		}
		_ = json.Unmarshal(raw, &hdr) //nolint:errcheck // best-effort id capture for logging
		res.Conversation.SourceUUID = hdr.UUID
		res.Conversation.UserID = userID
		return res, nil
	}

	var conv dumpConversation
	if err := json.Unmarshal(raw, &conv); err != nil {
		return res, fmt.Errorf("unmarshal conversation: %w", err)
	}

	created, createdOK := parseTime(conv.CreatedAt)
	if conv.CreatedAt != "" && !createdOK {
		res.ParseErrors++
	}
	title := strings.TrimSpace(conv.Name)
	if title == "" {
		if !created.IsZero() {
			title = "Untitled (" + created.Format("2006-01-02") + ")"
		} else {
			title = "Untitled"
		}
	}
	res.Conversation = domain.ImportedConversation{
		SourceUUID:  conv.UUID,
		UserID:      userID,
		AccountUUID: conv.Account.UUID,
		Title:       title,
		Summary:     strings.TrimSpace(conv.Summary),
		CreatedAt:   created,
	}
	if u, ok := parseTime(conv.UpdatedAt); ok {
		res.Conversation.UpdatedAt = u
	}

	// Order by created_at. The export is already chronological; a message with an
	// unparseable timestamp carries forward the previous message's time so it keeps
	// its array position under a stable sort instead of jumping to epoch.
	type ordered struct {
		idx int
		t   time.Time
	}
	items := make([]ordered, 0, len(conv.ChatMessages))
	var last time.Time
	for i := range conv.ChatMessages {
		m := &conv.ChatMessages[i]
		t, ok := parseTime(m.CreatedAt)
		if !ok {
			if m.CreatedAt != "" {
				res.ParseErrors++
			}
			t = last
		} else {
			last = t
		}
		items = append(items, ordered{idx: i, t: t})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].t.Before(items[j].t) })

	seq := 0
	for _, it := range items {
		m := &conv.ChatMessages[it.idx]
		content, skippedBin := reconstructMessage(m)
		res.SkippedBinaries += skippedBin
		if content == "" {
			res.SkippedEmpty++
			continue
		}
		res.Messages = append(res.Messages, domain.ImportedMessage{
			SourceUUID:        m.UUID,
			ConversationUUID:  conv.UUID,
			UserID:            userID,
			Sender:            m.Sender,
			Seq:               seq,
			Content:           content,
			ParentMessageUUID: m.ParentMessageUUID,
			CreatedAt:         it.t,
		})
		seq++
	}
	res.Conversation.MessageCount = len(res.Messages)
	return res, nil
}

// parseTime parses a Claude export timestamp (RFC3339 with optional sub-seconds and a
// trailing Z). ok is false for both empty and malformed input; callers distinguish
// the two by checking the raw string.
func parseTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	// RFC3339Nano's trailing-optional fraction also parses fraction-less RFC3339
	// timestamps, so it covers both "…:47.398804Z" and "…:47Z".
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, true
	}
	return time.Time{}, false
}

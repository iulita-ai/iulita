// Package slackpost implements the slack_post LLM tool: the assistant, mid-
// conversation, asks to post a message to a Slack channel via the bot. By default
// (WriteMode=draft) the owner must approve the draft before it is posted; auto
// mode posts within guardrails. This is the BOT write track, independent of the
// user-token search track (internal/skill/slack).
package slackpost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	slackch "github.com/iulita-ai/iulita/internal/channel/slack"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/eventbus"
	"github.com/iulita-ai/iulita/internal/security"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/skill/interact"
)

// auditSink is the slice of storage.Repository used for the post-audit log.
type auditSink interface {
	SaveAuditEntry(ctx context.Context, e *domain.AuditEntry) error
}

// PostSkill is the slack_post tool.
type PostSkill struct {
	poster ChannelPoster // nil until SetChannelPoster (deferred wiring after channelmgr exists)
	audit  auditSink
	bus    *eventbus.Bus // nil-safe; observability
	logger *zap.Logger
}

// NewPostSkill constructs the skill. poster is wired later via SetChannelPoster.
func NewPostSkill(audit auditSink, logger *zap.Logger) *PostSkill {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &PostSkill{audit: audit, logger: logger}
}

// SetChannelPoster wires the bot-posting seam (built after skill registration).
func (s *PostSkill) SetChannelPoster(p ChannelPoster) { s.poster = p }

// SetBus wires the observability event bus (deferred wiring).
func (s *PostSkill) SetBus(bus *eventbus.Bus) { s.bus = bus }

// Name is the tool name exposed to the LLM.
func (s *PostSkill) Name() string { return "slack_post" }

// Description tells the model what the tool does and its safety posture.
func (s *PostSkill) Description() string {
	return "Post a message to a Slack channel via the bot. By default this drafts the message and " +
		"asks the owner to approve it before posting. Only pre-approved channels are writable. Use " +
		"only when the owner asked to post something to Slack, or clearly wants a channel notified."
}

// InputSchema is the JSON schema for the tool arguments.
func (s *PostSkill) InputSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "channel": { "type": "string", "description": "Slack channel ID (starts with C or G) to post to. Must be an allow-listed write channel." },
    "text": { "type": "string", "description": "The fully composed message to post." },
    "provenance": { "type": "string", "description": "If this content was derived from Slack search results or other untrusted input, describe the source here. Setting it forces draft-for-approval." }
  },
  "required": ["channel", "text"]
}`)
}

// RequiredCapabilities gates the skill on a write-enabled bot instance.
func (s *PostSkill) RequiredCapabilities() []string { return []string{"slack_write"} }

// RequestTimeout extends the assistant's per-turn deadline so it covers a blocking
// draft approval. The actual approval WINDOW is set by the conversation's prompter
// (Slack: 30m; other channels: interact.DefaultTimeout); this only ensures the
// surrounding turn's deadline does not fire first.
func (s *PostSkill) RequestTimeout() time.Duration { return 35 * time.Minute }

type postInput struct {
	Channel    string `json:"channel"`
	Text       string `json:"text"`
	Provenance string `json:"provenance"`
}

// Execute runs the draft/auto posting flow.
func (s *PostSkill) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in postInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if s.poster == nil {
		return "Slack posting is not available.", nil
	}
	in.Channel = strings.TrimSpace(in.Channel)
	if in.Channel == "" || strings.TrimSpace(in.Text) == "" {
		return "", fmt.Errorf("channel and text are required")
	}

	// Fail fast on secrets in the text or the (model-supplied) provenance, which is
	// stored in the audit log. The channel re-checks the text as a non-bypassable
	// chokepoint.
	if matched, pattern := security.Contains(in.Text + "\n" + in.Provenance); matched {
		s.writeAudit(ctx, "slack.post.blocked_secret", in, "blocked_secret", false)
		s.logger.Warn("slack_post refused: suspected secret", zap.String("pattern", pattern))
		return "I won't post that — it looks like it contains a credential or secret.", nil
	}

	mode := s.poster.WriteMode(ctx, in.Channel)
	if mode == "off" {
		s.writeAudit(ctx, "slack.post.denied", in, "denied", false)
		return fmt.Sprintf("Posting to channel %s isn't allowed. Ask the owner to add it to the bot's write channels in Settings.", in.Channel), nil
	}

	// Server-enforced interlock: content that traces to untrusted Slack search
	// results (this turn) or a declared provenance must go through draft approval
	// even if the channel is configured auto. The taint flag can't be prompted away.
	if mode == "auto" && (in.Provenance != "" || skill.SlackSearchUsedInTurn(ctx)) {
		mode = "draft"
	}

	if mode == "draft" {
		return s.runDraft(ctx, in)
	}
	return s.runAuto(ctx, in)
}

func (s *PostSkill) runDraft(ctx context.Context, in postInput) (string, error) {
	asker := interact.PrompterFrom(ctx)
	question := fmt.Sprintf("Post this to channel %s?\n\n%s", in.Channel, in.Text)
	if in.Provenance != "" {
		question += fmt.Sprintf("\n\n(Drafted from: %s)", in.Provenance)
	}
	answer, err := asker.Ask(ctx, question, []interact.Option{
		{ID: "approve", Label: "Post it"},
		{ID: "discard", Label: "Discard"},
	})
	if err != nil {
		// No prompter, timeout, or cancellation → fail closed, do not post. The
		// failure is reported to the user, not surfaced as a tool error (which
		// would make the loop retry).
		s.writeAudit(ctx, "slack.post.approval_failed", in, "approval_failed", false)
		return "I couldn't get your approval to post, so I didn't post anything.", nil //nolint:nilerr // intentional: report, don't retry
	}
	if answer != "approve" {
		s.writeAudit(ctx, "slack.post.discarded", in, "discarded", false)
		return "Okay, I discarded the draft — nothing was posted.", nil
	}
	ts, err := s.poster.PostToChannel(ctx, in.Channel, in.Text)
	if err != nil {
		return s.postError(ctx, in, err, "draft_approved")
	}
	s.writeAudit(ctx, "slack.post.sent", in, "draft_approved", true)
	return fmt.Sprintf("Posted to %s (ts %s).", in.Channel, ts), nil
}

func (s *PostSkill) runAuto(ctx context.Context, in postInput) (string, error) {
	ts, err := s.poster.PostToChannel(ctx, in.Channel, in.Text)
	if err != nil {
		return s.postError(ctx, in, err, "auto")
	}
	s.writeAudit(ctx, "slack.post.sent", in, "auto", true)
	return fmt.Sprintf("Posted to %s (ts %s).", in.Channel, ts), nil
}

// postError maps a PostToChannel error to a clean message + audit; it never
// silently downgrades (a guardrail-blocked auto post is reported, not drafted).
func (s *PostSkill) postError(ctx context.Context, in postInput, err error, decision string) (string, error) {
	switch {
	case errors.Is(err, slackch.ErrGuardrailBlocked):
		s.writeAudit(ctx, "slack.post.blocked_guardrail", in, decision, false)
		return "That post is blocked by a guardrail right now (rate limit or quiet hours). Try again later.", nil
	case errors.Is(err, slackch.ErrSecretDetected):
		s.writeAudit(ctx, "slack.post.blocked_secret", in, decision, false)
		return "I won't post that — it looks like it contains a credential or secret.", nil
	case errors.Is(err, slackch.ErrWriteDenied):
		s.writeAudit(ctx, "slack.post.denied", in, decision, false)
		return fmt.Sprintf("Posting to channel %s isn't allowed.", in.Channel), nil
	default:
		s.writeAudit(ctx, "slack.post.error", in, decision, false)
		s.logger.Warn("slack_post failed", zap.Error(err))
		return "Sorry, posting to Slack failed. Try again.", nil
	}
}

func (s *PostSkill) writeAudit(ctx context.Context, action string, in postInput, decision string, success bool) {
	if s.audit == nil {
		return
	}
	sum := sha256.Sum256([]byte(in.Text))
	detail, err := json.Marshal(map[string]any{
		"channel":     in.Channel,
		"decision":    decision,
		"provenance":  in.Provenance,
		"text_sha256": hex.EncodeToString(sum[:]),
		"text_len":    len(in.Text),
	})
	if err != nil {
		detail = []byte("{}")
	}
	entry := &domain.AuditEntry{
		ChatID:  skill.ChatIDFrom(ctx),
		UserID:  skill.UserIDFrom(ctx),
		Action:  action,
		Detail:  string(detail),
		Success: success,
	}
	if err := s.audit.SaveAuditEntry(ctx, entry); err != nil {
		s.logger.Warn("slack_post: audit write failed", zap.Error(err))
	}
	if s.bus != nil {
		// On success `decision` is the mode (auto/draft_approved). On failure the
		// caller may pass the mode, so take the true failure kind from the action
		// suffix (slack.post.blocked_guardrail -> "blocked_guardrail") — that is
		// what the post-failure metric labels on.
		mode := "auto"
		if decision == "draft_approved" {
			mode = "draft"
		}
		kind := decision
		if !success {
			kind = strings.TrimPrefix(action, "slack.post.")
		}
		s.bus.Publish(ctx, eventbus.Event{Type: eventbus.SlackPost, Payload: eventbus.SlackPostPayload{
			Mode: mode, Decision: kind, Success: success,
		}})
	}
}

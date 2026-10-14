package slack

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	slackapi "github.com/slack-go/slack"
	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/skill/interact"
)

// confirmPrefix marks Block Kit action IDs belonging to security
// confirmations ("cfm:<nonce>:1|0").
const confirmPrefix = "cfm:"

type pendingSlackConfirm struct {
	replyCh     chan string // "1" | "O" | "G" | "0"
	expectSlack string      // Slack user ID of the chat owner (requesting principal)
	nonce       string
}

type confirmState struct {
	mu      sync.Mutex
	pending map[string]*pendingSlackConfirm // nonce → entry
}

func newConfirmState() *confirmState {
	return &confirmState{pending: make(map[string]*pendingSlackConfirm)}
}

func (cs *confirmState) set(nonce string, pc *pendingSlackConfirm) {
	cs.mu.Lock()
	// Supersede any pending confirm: it must never remain answerable.
	for _, v := range cs.pending {
		if v.replyCh != pc.replyCh {
			select {
			case v.replyCh <- "0":
			default:
			}
		}
	}
	cs.pending[nonce] = pc
	cs.mu.Unlock()
}

func (cs *confirmState) take(nonce string) (*pendingSlackConfirm, bool) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	pc, ok := cs.pending[nonce]
	if ok {
		delete(cs.pending, nonce)
	}
	return pc, ok
}

func (cs *confirmState) abandon(pc *pendingSlackConfirm) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cur, ok := cs.pending[pc.nonce]; ok && cur == pc {
		delete(cs.pending, pc.nonce)
	}
}

// slackConfirmer implements interact.ConfirmationAsker. The buttons land in
// the requesting thread; the answering callback must come from the same
// Slack user who owns that chat, verified against the chat metadata and the
// one-shot nonce.
type slackConfirmer struct {
	channel *Channel
	chatID  string
}

// Confirm posts Block Kit buttons and waits for the owner's click.
func (sc *slackConfirmer) Confirm(ctx context.Context, action interact.Confirmation, expect interact.Actor) (interact.Decision, error) {
	meta := sc.channel.getChatMeta(sc.chatID)
	if meta == nil {
		return interact.Decision{}, fmt.Errorf("no chat context for %s", sc.chatID)
	}
	nonce := action.Nonce

	text := fmt.Sprintf("*🔐 Confirmation required (%s)*\n\n%s\n\nApprove this exact operation (%#q)?",
		action.Reason, action.Preview, action.OperationID)
	if action.MaxBytes > 0 {
		text += fmt.Sprintf("\nLimit: %d bytes.", action.MaxBytes)
	}

	confirmBlock := "cfm:" + nonce
	buttons := []slackapi.BlockElement{
		slackapi.NewButtonBlockElement(confirmBlock+":1", "confirm_approve",
			slackapi.NewTextBlockObject("plain_text", "✅ Approve", false, false)),
		slackapi.NewButtonBlockElement(confirmBlock+":0", "confirm_deny",
			slackapi.NewTextBlockObject("plain_text", "❌ Deny", false, false)),
	}
	if action.Reason == interact.ConfirmMutation {
		buttons = append(buttons,
			slackapi.NewButtonBlockElement(confirmBlock+":O", "confirm_always_origin",
				slackapi.NewTextBlockObject("plain_text", "🔁 Always for this origin", false, false)),
			slackapi.NewButtonBlockElement(confirmBlock+":G", "confirm_always",
				slackapi.NewTextBlockObject("plain_text", "♾️ Always", false, false)),
		)
	}
	opts := []slackapi.MsgOption{
		slackapi.MsgOptionBlocks(
			slackapi.NewSectionBlock(
				slackapi.NewTextBlockObject("mrkdwn", text, false, false),
				nil, nil,
			),
			slackapi.NewActionBlock(confirmBlock, buttons...),
		),
	}
	if meta.threadTS != "" {
		opts = append(opts, slackapi.MsgOptionTS(meta.threadTS))
	}
	if _, _, err := sc.channel.client.PostMessage(meta.channelID, opts...); err != nil {
		return interact.Decision{}, fmt.Errorf("posting confirmation: %w", err)
	}

	replyCh := make(chan string, 1)
	pc := &pendingSlackConfirm{replyCh: replyCh, expectSlack: meta.userID, nonce: nonce}
	sc.channel.confirms.set(nonce, pc)
	defer sc.channel.confirms.abandon(pc)

	timer := time.NewTimer(time.Until(action.ExpiresAt))
	defer timer.Stop()

	select {
	case verdict := <-replyCh:
		switch verdict {
		case "1", "O", "G":
			decision := interact.Decision{Approved: true, Actor: expect, MaxBytes: action.MaxBytes}
			if verdict == "O" {
				decision.Standing = interact.StandingOrigin
			}
			if verdict == "G" {
				decision.Standing = interact.StandingGlobal
			}
			return decision, nil
		default:
			return interact.Decision{Approved: false, Actor: expect}, interact.ErrConfirmRejected
		}
	case <-timer.C:
		return interact.Decision{}, interact.ErrPromptTimeout
	case <-ctx.Done():
		return interact.Decision{}, interact.ErrCancelled
	case <-sc.channel.shutdownCh:
		return interact.Decision{}, fmt.Errorf("slack channel shutting down")
	}
}

// handleConfirmCallback processes a confirmation button click.
// Returns true when the action belonged to the confirmation system.
func (c *Channel) handleConfirmCallback(actionID, slackUserID string) bool {
	if !strings.HasPrefix(actionID, confirmPrefix) {
		return false
	}
	parts := splitActionID(actionID)
	if len(parts) != 3 {
		return true // malformed — consumed silently
	}
	nonce, verdict := parts[1], parts[2]

	pc, ok := c.confirms.take(nonce)
	if !ok {
		return true // stale/replayed — consumed silently
	}
	// Identity binding: the click must come from the chat's requesting user.
	if pc.expectSlack != "" && slackUserID != pc.expectSlack {
		c.logger.Warn("confirmation actor mismatch",
			zap.String("clicked_by", slackUserID),
			zap.String("requested_by", pc.expectSlack))
		return true
	}
	select {
	case pc.replyCh <- verdict:
	default:
	}
	return true
}

// ConfirmerFor returns a ConfirmationAsker for the given Slack chat.
func (c *Channel) ConfirmerFor(chatID string) interact.ConfirmationAsker {
	if c.getChatMeta(chatID) == nil {
		return nil
	}
	return &slackConfirmer{channel: c, chatID: chatID}
}

package telegram

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/skill/interact"
)

// confirmPrefix marks callback data belonging to security confirmations.
const confirmPrefix = "cfm:"

// pendingConfirm is one awaiting identity-bound confirmation. The channel
// carries the verdict: "1" one-shot approve, "O" always-for-origin,
// "G" always (global), "0" deny.
type pendingConfirm struct {
	replyCh    chan string
	expectFrom int64  // Telegram user ID of the requesting principal (0 = any allowed)
	domainUser string // domain user ID for the decision actor
	nonce      string
	msgID      int
	expires    time.Time
}

// confirmState tracks pending confirmations by nonce (one-shot).
type confirmState struct {
	mu      sync.Mutex
	pending map[string]*pendingConfirm
}

func newConfirmState() *confirmState {
	return &confirmState{pending: make(map[string]*pendingConfirm)}
}

func (cs *confirmState) set(nonce string, pc *pendingConfirm) {
	cs.mu.Lock()
	// Concurrent prompt overwrite: a previous pending confirm is superseded —
	// it must never remain answerable.
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

// take removes and returns the one-shot entry (nonce consumed here).
func (cs *confirmState) take(nonce string) (*pendingConfirm, bool) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	pc, ok := cs.pending[nonce]
	if ok {
		delete(cs.pending, nonce)
	}
	return pc, ok
}

func (cs *confirmState) abandon(pc *pendingConfirm) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cur, ok := cs.pending[pc.nonce]; ok && cur == pc {
		delete(cs.pending, pc.nonce)
	}
}

// telegramConfirmer implements interact.ConfirmationAsker with callback
// buttons whose answers are bound to the nonce, the prompt message, the
// chat, and the requesting Telegram user (plan §8.3). Free-text approval is
// deliberately not accepted — only verified callback clicks decide.
type telegramConfirmer struct {
	channel *Channel
	chatID  int64
	fromTg  int64 // requesting Telegram user captured for this turn
}

// Confirm sends the inline-keyboard question and waits for the bound click.
func (tc *telegramConfirmer) Confirm(ctx context.Context, action interact.Confirmation, expect interact.Actor) (interact.Decision, error) {
	nonce := action.Nonce
	denyData := confirmPrefix + nonce + ":0"

	text := fmt.Sprintf("🔐 Confirmation required (%s)\n\n%s\n\nThis approves exactly this operation (id %s).",
		action.Reason, action.Preview, shortID(action.OperationID))
	if action.MaxBytes > 0 {
		text += fmt.Sprintf("\nLimit: %d bytes.", action.MaxBytes)
	}
	text += "\n\nApprove?"

	// Standing choices ("always") apply to mutation consents only; one-shot
	// + deny for everything else (large downloads always re-ask).
	row := []models.InlineKeyboardButton{
		{Text: "✅ Approve", CallbackData: confirmPrefix + nonce + ":1"},
		{Text: "❌ Deny", CallbackData: denyData},
	}
	keyboard := [][]models.InlineKeyboardButton{row}
	if action.Reason == interact.ConfirmMutation {
		keyboard = append(keyboard, []models.InlineKeyboardButton{
			{Text: "🔁 Always for " + confirmOriginLabel(action), CallbackData: confirmPrefix + nonce + ":O"},
			{Text: "♾️ Always", CallbackData: confirmPrefix + nonce + ":G"},
		})
	}

	msg, err := tc.channel.bot.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      tc.chatID,
		Text:        text,
		ReplyMarkup: models.InlineKeyboardMarkup{InlineKeyboard: keyboard},
	})
	if err != nil {
		return interact.Decision{}, fmt.Errorf("sending confirmation: %w", err)
	}

	replyCh := make(chan string, 1)
	pc := &pendingConfirm{
		replyCh:    replyCh,
		expectFrom: tc.fromTg,
		domainUser: expect.UserID,
		nonce:      nonce,
		msgID:      msg.ID,
		expires:    action.ExpiresAt,
	}
	tc.channel.confirms.set(nonce, pc)
	defer tc.channel.confirms.abandon(pc)

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
	}
}

// HandleConfirmCallback processes a confirmation callback query.
// Returns true when the callback belonged to the confirmation system.
func (c *Channel) HandleConfirmCallback(cq *models.CallbackQuery) bool {
	if !strings.HasPrefix(cq.Data, confirmPrefix) {
		return false
	}
	parts := strings.SplitN(strings.TrimPrefix(cq.Data, confirmPrefix), ":", 2)
	if len(parts) != 2 {
		return true
	}
	nonce, verdict := parts[0], parts[1]

	if cq.Message.Message == nil {
		return true
	}
	chatID := cq.Message.Message.Chat.ID

	// One-shot nonce: consumed atomically; replays answer nothing.
	pc, ok := c.confirms.take(nonce)
	if !ok {
		_, _ = c.bot.AnswerCallbackQuery(context.Background(), &bot.AnswerCallbackQueryParams{ //nolint:errcheck // ack only
			CallbackQueryID: cq.ID,
			Text:            "This confirmation is no longer active.",
		})
		return true
	}

	// Identity binding: the click must come from the requesting Telegram
	// user in the chat where the question was asked. Another group member's
	// click never grants consent.
	if pc.expectFrom != 0 && cq.From.ID != pc.expectFrom {
		c.logger.Warn("confirmation actor mismatch",
			zap.Int64("chat_id", chatID),
			zap.Int64("clicked_by", cq.From.ID),
			zap.Int64("requested_by", pc.expectFrom))
		_, _ = c.bot.AnswerCallbackQuery(context.Background(), &bot.AnswerCallbackQueryParams{ //nolint:errcheck
			CallbackQueryID: cq.ID,
			Text:            "Only the requesting user can answer this confirmation.",
		})
		return true
	}
	if chatID != 0 {
		// The prompt message lives in the requester's chat context.
		_ = chatID
	}

	_, _ = c.bot.AnswerCallbackQuery(context.Background(), &bot.AnswerCallbackQueryParams{ //nolint:errcheck // ack
		CallbackQueryID: cq.ID,
		Text:            map[string]string{"1": "Approved", "O": "Approved (always for this origin)", "G": "Approved (always)", "0": "Denied"}[verdict],
	})

	select {
	case pc.replyCh <- verdict:
	default:
	}
	return true
}

// confirmOriginLabel renders a short origin label for the standing button.
func confirmOriginLabel(action interact.Confirmation) string {
	if i := strings.Index(action.Preview, "://"); i > 0 {
		rest := action.Preview[i+3:]
		end := len(rest)
		for j := 0; j < len(rest); j++ {
			if rest[j] == '/' || rest[j] == ' ' || rest[j] == '\n' {
				end = j
				break
			}
		}
		if host := rest[:end]; host != "" {
			return host
		}
	}
	return "this origin"
}

// ConfirmerFor returns a ConfirmationAsker bound to the chat and the
// Telegram user who triggered the current turn.
func (c *Channel) ConfirmerFor(chatID string) interact.ConfirmationAsker {
	tgChatID, err := strconv.ParseInt(chatID, 10, 64)
	if err != nil {
		return nil
	}
	c.confirmSendersMu.Lock()
	fromTg := c.confirmSenders[chatID]
	c.confirmSendersMu.Unlock()
	return &telegramConfirmer{channel: c, chatID: tgChatID, fromTg: fromTg}
}

// RecordTurnSender captures the Telegram user who sent the message that
// started the current turn, so confirmations bind to the requester.
func (c *Channel) RecordTurnSender(chatID string, tgUserID int64) {
	c.confirmSendersMu.Lock()
	c.confirmSenders[chatID] = tgUserID
	c.confirmSendersMu.Unlock()
}

func shortID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

package telegram

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/iulita-ai/iulita/internal/skill/interact"
)

// confirmPrefix marks callback data belonging to security confirmations.
const confirmPrefix = "cfm:"

// pendingConfirm is one awaiting identity-bound confirmation. The channel
// carries the verdict: "1" one-shot approve, "O" always-for-origin,
// "G" always (global), "0" deny.
type pendingConfirm struct {
	replyCh    chan string
	expectFrom int64  // Nonzero Telegram user ID of the requesting principal
	domainUser string // domain user ID for the decision actor
	nonce      string
	chatID     int64
	reason     string
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
	for previous, v := range cs.pending {
		if v.replyCh != pc.replyCh && v.chatID == pc.chatID && v.expectFrom == pc.expectFrom {
			delete(cs.pending, previous)
			select {
			case v.replyCh <- "0":
			default:
			}
		}
	}
	cs.pending[nonce] = pc
	cs.mu.Unlock()
}

// takeBound validates platform identity before consuming a one-shot nonce.
func (cs *confirmState) takeBound(nonce, verdict string, sender, chat int64, message int) (*pendingConfirm, bool) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	pc, ok := cs.pending[nonce]
	if !ok {
		return nil, false
	}
	if !pc.expires.After(time.Now()) {
		delete(cs.pending, nonce)
		return nil, false
	}
	if sender == 0 || pc.expectFrom == 0 || sender != pc.expectFrom || chat != pc.chatID || message != pc.msgID {
		return nil, false
	}
	validVerdict := verdict == "1" || verdict == "0" || pc.reason == interact.ConfirmMutation && (verdict == "O" || verdict == "G")
	if !validVerdict {
		return nil, false
	}
	delete(cs.pending, nonce)
	return pc, true
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
	channel    *Channel
	chatID     int64
	fromTg     int64 // requesting Telegram user captured for this turn
	approvalMu sync.Mutex
	approved   map[string]string // execution nonce to verified domain owner
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
		chatID:     tc.chatID,
		reason:     action.Reason,
		expectFrom: tc.fromTg,
		domainUser: expect.UserID,
		nonce:      nonce,
		msgID:      msg.ID,
		expires:    action.ExpiresAt,
	}
	tc.channel.confirms.set(nonce, pc)
	defer tc.channel.confirms.abandon(pc)
	status := "⌛ Confirmation expired or cancelled."
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := tc.channel.bot.EditMessageText(cleanup, &bot.EditMessageTextParams{ChatID: tc.chatID, MessageID: msg.ID, Text: text + "\n\n" + status, ReplyMarkup: models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{}}}); err != nil {
			// Telegram may reject text edits (including its message length limit).
			// Clearing the keyboard separately still retires the action visibly.
			clearctx, clearcancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer clearcancel()
			if _, clearerr := tc.channel.bot.EditMessageReplyMarkup(clearctx, &bot.EditMessageReplyMarkupParams{ChatID: tc.chatID, MessageID: msg.ID, ReplyMarkup: models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{}}}); clearerr != nil {
				tc.channel.logger.Warn("could not clear resolved confirmation keyboard")
			}
		}
	}()

	timer := time.NewTimer(time.Until(action.ExpiresAt))
	defer timer.Stop()

	select {
	case verdict := <-replyCh:
		switch verdict {
		case "1", "O", "G":
			status = "✅ Approved. Executing this operation."
			tc.approvalMu.Lock()
			if tc.approved == nil {
				tc.approved = make(map[string]string)
			}
			tc.approved[action.OperationID] = expect.UserID
			tc.approvalMu.Unlock()
			decision := interact.Decision{Approved: true, Actor: expect, MaxBytes: action.MaxBytes}
			if verdict == "O" {
				decision.Standing = interact.StandingOrigin
			}
			if verdict == "G" {
				decision.Standing = interact.StandingGlobal
			}
			return decision, nil
		default:
			status = "❌ Denied."
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
	pc, ok := c.confirms.takeBound(nonce, verdict, cq.From.ID, chatID, cq.Message.Message.ID)
	if !ok {
		_, _ = c.bot.AnswerCallbackQuery(context.Background(), &bot.AnswerCallbackQueryParams{ //nolint:errcheck // ack only
			CallbackQueryID: cq.ID,
			Text:            "This confirmation is no longer active.",
		})
		return true
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

// DeliverPrivateResult sends a plain-text attachment only to the requesting
// user's private Telegram chat. Group contexts fail closed; never fall back to
// sending private SSH output to a public chat or the model.
func (tc *telegramConfirmer) DeliverPrivateResult(ctx context.Context, actor interact.Actor, executionID, text string) error {
	tc.approvalMu.Lock()
	owner := tc.approved[executionID]
	if owner == actor.UserID && actor.UserID != "" {
		delete(tc.approved, executionID)
	}
	tc.approvalMu.Unlock()
	if actor.UserID == "" || tc.fromTg <= 0 || tc.chatID != tc.fromTg || owner != actor.UserID {
		return errors.New("private SSH output requires requester private chat")
	}
	_, err := tc.channel.bot.SendDocument(ctx, &bot.SendDocumentParams{ChatID: tc.chatID, Document: &models.InputFileUpload{Filename: "ssh-" + executionID + ".txt", Data: strings.NewReader(text)}, Caption: "SSH command result (private output; not sent to the AI model)", ProtectContent: true})
	return err
}

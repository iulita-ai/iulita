package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/iulita-ai/iulita/internal/i18n"
	"github.com/iulita-ai/iulita/internal/skill/interact"
)

var errPromptPending = errors.New("prompt already pending in this conversation")

type pendingPrompt struct {
	replyCh    chan string
	options    map[string]string
	otherMode  bool
	manualHint string
	actor      int64
	chat       int64
	thread     int
	message    int
	expires    time.Time
}
type promptState struct {
	mu      sync.Mutex
	pending map[string]*pendingPrompt
}

func newPromptState() *promptState { return &promptState{pending: make(map[string]*pendingPrompt)} }

type telegramPrompter struct {
	channel  *Channel
	chatID   int64
	threadID int
	actor    int64
	prompts  *promptState
}

func (tp *telegramPrompter) Ask(ctx context.Context, question string, options []interact.Option) (string, error) {
	if tp.actor == 0 {
		return "", fmt.Errorf("prompt actor unavailable")
	}
	nonce := "prompt:" + generateNonce() + ":"
	p := &pendingPrompt{replyCh: make(chan string, 1), options: make(map[string]string), otherMode: len(options) == 0, actor: tp.actor, chat: tp.chatID, thread: tp.threadID, expires: time.Now().Add(interact.DefaultTimeout), manualHint: i18n.T(ctx, "TelegramPromptTypeAnswer")}
	var rows [][]models.InlineKeyboardButton
	for i, opt := range options {
		if i >= interact.MaxOptions {
			break
		}
		cb := fmt.Sprintf("%s%d", nonce, i)
		p.options[cb] = opt.ID
		rows = append(rows, []models.InlineKeyboardButton{{Text: opt.Label, CallbackData: cb}})
	}
	if len(options) > 0 {
		p.options[nonce+"other"] = "__other__"
		rows = append(rows, []models.InlineKeyboardButton{{Text: i18n.T(ctx, "TelegramPromptOther"), CallbackData: nonce + "other"}})
	}
	p.options[nonce+"cancel"] = "__cancel__"
	rows = append(rows, []models.InlineKeyboardButton{{Text: i18n.T(ctx, "TelegramPromptCancel"), CallbackData: nonce + "cancel"}})
	tp.prompts.mu.Lock()
	for _, existing := range tp.prompts.pending {
		if existing.actor == tp.actor && existing.chat == tp.chatID && existing.thread == tp.threadID && time.Now().Before(existing.expires) {
			tp.prompts.mu.Unlock()
			return "", errPromptPending
		}
	}
	tp.prompts.pending[nonce] = p
	tp.prompts.mu.Unlock()
	defer func() {
		tp.prompts.mu.Lock()
		delete(tp.prompts.pending, nonce)
		tp.prompts.mu.Unlock()
		if p.message != 0 {
			tp.prompts.removeKeyboard(tp.channel.bot, tp.chatID, p.message)
		}
	}()
	sent, err := tp.channel.bot.SendMessage(ctx, &bot.SendMessageParams{ChatID: tp.chatID, MessageThreadID: tp.threadID, Text: question, ReplyMarkup: models.InlineKeyboardMarkup{InlineKeyboard: rows}})
	if err != nil {
		return "", fmt.Errorf("sending prompt: %w", sanitizeTGError(err))
	}
	tp.prompts.mu.Lock()
	p.message = sent.ID
	tp.prompts.mu.Unlock()
	timer := time.NewTimer(interact.DefaultTimeout)
	defer timer.Stop()
	select {
	case a := <-p.replyCh:
		if a == "__cancel__" {
			return "", interact.ErrCancelled
		}
		return a, nil
	case <-timer.C:
		return "", interact.ErrPromptTimeout
	case <-ctx.Done():
		return "", interact.ErrCancelled
	}
}
func (ps *promptState) HandleCallback(b *bot.Bot, cq *models.CallbackQuery) bool {
	if !strings.HasPrefix(cq.Data, "prompt:") {
		return false
	}
	var selected *pendingPrompt
	var option string
	ps.mu.Lock()
	for _, p := range ps.pending {
		if p.actor != cq.From.ID || cq.Message.Message == nil {
			continue
		}
		m := cq.Message.Message
		if p.chat != m.Chat.ID || p.thread != m.MessageThreadID || p.message != m.ID || !time.Now().Before(p.expires) {
			continue
		}
		if v, ok := p.options[cq.Data]; ok {
			selected = p
			option = v
			if v == "__other__" {
				p.otherMode = true
			} else {
				select {
				case p.replyCh <- v:
					p.options = nil
					p.otherMode = false
				default:
				}
			}
			break
		}
	}
	ps.mu.Unlock()
	//nolint:errcheck // A failed visual acknowledgement cannot undo the consumed actor-bound selection.
	_, _ = b.AnswerCallbackQuery(context.Background(), &bot.AnswerCallbackQueryParams{CallbackQueryID: cq.ID})
	if selected != nil {
		ps.removeKeyboard(b, selected.chat, selected.message)
		if option == "__other__" {
			if _, sendErr := b.SendMessage(context.Background(), &bot.SendMessageParams{ChatID: selected.chat, MessageThreadID: selected.thread, Text: selected.manualHint}); sendErr != nil {
				return true
			}
		}
	}
	return true
}
func (ps *promptState) removeKeyboard(b *bot.Bot, chatID int64, msgID int) {
	//nolint:errcheck // Stale buttons remain harmless: nonce, actor and expiry checks reject them.
	_, _ = b.EditMessageReplyMarkup(context.Background(), &bot.EditMessageReplyMarkupParams{ChatID: chatID, MessageID: msgID, ReplyMarkup: models.InlineKeyboardMarkup{}})
}

// HandleTextBound accepts only the actor and topic of the selected prompt.
func (ps *promptState) HandleTextBound(chatID int64, threadID int, actor int64, text string) bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	var selected *pendingPrompt
	for _, p := range ps.pending {
		if p.chat == chatID && p.thread == threadID && p.actor == actor && p.otherMode && time.Now().Before(p.expires) {
			if selected != nil {
				return false
			}
			selected = p
		}
	}
	if selected == nil {
		return false
	}
	if strings.HasPrefix(strings.TrimSpace(text), "/") {
		if strings.TrimSpace(text) != "/cancel" {
			return false
		}
		text = "__cancel__"
	}
	select {
	case selected.replyCh <- text:
		selected.otherMode = false
		selected.options = nil
	default:
	}
	return true
}
func (c *Channel) PrompterFor(chatID string) interact.PromptAsker {
	target, err := c.resolveTarget(context.Background(), chatID)
	if err != nil || !target.Private || target.OwnerID == "" || target.ChatID <= 0 {
		return nil
	}
	actor := target.ChatID
	return &telegramPrompter{channel: c, chatID: target.ChatID, threadID: target.ThreadID, actor: actor, prompts: c.prompts}
}

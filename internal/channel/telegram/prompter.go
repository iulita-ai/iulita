package telegram

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/iulita-ai/iulita/internal/skill/interact"
)

// pendingPrompt tracks a blocking prompt waiting for user response.
type pendingPrompt struct {
	replyCh   chan string
	options   map[string]string // callbackData → optionID
	otherMode bool              // true = next plain text goes to replyCh
}

// promptState manages pending interactive prompts per chat.
type promptState struct {
	mu      sync.Mutex
	pending map[int64]*pendingPrompt // tgChatID → pending
}

func newPromptState() *promptState {
	return &promptState{pending: make(map[int64]*pendingPrompt)}
}

func (ps *promptState) set(chatID int64, p *pendingPrompt) {
	ps.mu.Lock()
	ps.pending[chatID] = p
	ps.mu.Unlock()
}

func (ps *promptState) get(chatID int64) (*pendingPrompt, bool) {
	ps.mu.Lock()
	p, ok := ps.pending[chatID]
	ps.mu.Unlock()
	return p, ok
}

func (ps *promptState) delete(chatID int64) {
	ps.mu.Lock()
	delete(ps.pending, chatID)
	ps.mu.Unlock()
}

// telegramPrompter implements interact.PromptAsker for Telegram using inline keyboards.
type telegramPrompter struct {
	channel *Channel
	chatID  int64
	prompts *promptState
}

func (tp *telegramPrompter) Ask(ctx context.Context, question string, options []interact.Option) (string, error) {
	replyCh := make(chan string, 1)

	// Build inline keyboard.
	var rows [][]models.InlineKeyboardButton
	optionMap := make(map[string]string) // callbackData → optionID

	for i, opt := range options {
		if i >= interact.MaxOptions {
			break
		}
		cbData := fmt.Sprintf("prompt_%d", i)
		optionMap[cbData] = opt.ID
		rows = append(rows, []models.InlineKeyboardButton{
			{Text: opt.Label, CallbackData: cbData},
		})
	}

	// Add "Enter manually" button.
	otherCB := "prompt_other"
	optionMap[otherCB] = "__other__"
	rows = append(rows, []models.InlineKeyboardButton{
		{Text: "✏️ Enter manually", CallbackData: otherCB},
	})

	_, err := tp.channel.bot.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      tp.chatID,
		Text:        question,
		ReplyMarkup: models.InlineKeyboardMarkup{InlineKeyboard: rows},
	})
	if err != nil {
		return "", fmt.Errorf("sending prompt: %w", err)
	}

	tp.prompts.set(tp.chatID, &pendingPrompt{
		replyCh: replyCh,
		options: optionMap,
	})
	defer tp.prompts.delete(tp.chatID)

	timeout := interact.DefaultTimeout
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case answer := <-replyCh:
		return answer, nil
	case <-timer.C:
		return "", interact.ErrPromptTimeout
	case <-ctx.Done():
		return "", interact.ErrCancelled
	}
}

// HandleCallback processes an inline keyboard callback query.
// Returns true if the callback was handled by the prompt system.
func (ps *promptState) HandleCallback(b *bot.Bot, cq *models.CallbackQuery) bool {
	if cq.Message.Message == nil {
		return false
	}
	chatID := cq.Message.Message.Chat.ID
	p, ok := ps.get(chatID)
	if !ok {
		return false
	}

	// Acknowledge the callback.
	_, _ = b.AnswerCallbackQuery(context.Background(), &bot.AnswerCallbackQueryParams{ //nolint:errcheck // ack only
		CallbackQueryID: cq.ID,
	})

	optionID, exists := p.options[cq.Data]
	if !exists {
		return true // consumed but unrecognized data
	}

	if optionID == "__other__" {
		// Switch to free-text mode — next plain message goes to replyCh.
		ps.mu.Lock()
		p.otherMode = true
		ps.mu.Unlock()

		// Remove inline keyboard and show hint.
		ps.removeKeyboard(b, chatID, cq.Message.Message.ID)
		_, _ = b.SendMessage(context.Background(), &bot.SendMessageParams{ //nolint:errcheck // hint
			ChatID: chatID,
			Text:   "Type your answer:",
		})
		return true
	}

	// Remove inline keyboard after selection.
	ps.removeKeyboard(b, chatID, cq.Message.Message.ID)

	// Send the selected option ID.
	select {
	case p.replyCh <- optionID:
	default:
	}
	return true
}

// removeKeyboard strips the inline keyboard from a message.
func (ps *promptState) removeKeyboard(b *bot.Bot, chatID int64, msgID int) {
	_, _ = b.EditMessageReplyMarkup(context.Background(), &bot.EditMessageReplyMarkupParams{ //nolint:errcheck // best-effort UI cleanup
		ChatID:      chatID,
		MessageID:   msgID,
		ReplyMarkup: models.InlineKeyboardMarkup{},
	})
}

// HandleText checks if a plain text message should be routed to a pending prompt.
// Returns true if the text was consumed by the prompt system.
func (ps *promptState) HandleText(chatID int64, text string) bool {
	p, ok := ps.get(chatID)
	if !ok {
		return false
	}

	ps.mu.Lock()
	isOther := p.otherMode
	ps.mu.Unlock()

	if !isOther {
		return false
	}

	select {
	case p.replyCh <- text:
	default:
	}
	return true
}

// PrompterFor creates a PromptAsker for the given chatID.
// Returns nil if the chatID is not a numeric Telegram chat ID.
func (c *Channel) PrompterFor(chatID string) interact.PromptAsker {
	tgChatID, err := strconv.ParseInt(chatID, 10, 64)
	if err != nil {
		return nil
	}
	return &telegramPrompter{
		channel: c,
		chatID:  tgChatID,
		prompts: c.prompts,
	}
}

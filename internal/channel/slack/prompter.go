package slack

import (
	"context"
	"fmt"
	"sync"

	slackapi "github.com/slack-go/slack"

	"github.com/iulita-ai/iulita/internal/skill/interact"
)

// promptState manages pending interactive prompts for Slack.
type promptState struct {
	mu      sync.Mutex
	pending map[string]chan string // actionID → reply channel
}

func newPromptState() *promptState {
	return &promptState{
		pending: make(map[string]chan string),
	}
}

func (ps *promptState) register(actionID string) chan string {
	ch := make(chan string, 1)
	ps.mu.Lock()
	ps.pending[actionID] = ch
	ps.mu.Unlock()
	return ch
}

func (ps *promptState) resolve(actionID, value string) bool {
	ps.mu.Lock()
	ch, ok := ps.pending[actionID]
	if ok {
		delete(ps.pending, actionID)
	}
	ps.mu.Unlock()

	if ok {
		ch <- value
		return true
	}
	return false
}

// slackPrompter implements interact.PromptAsker for a specific Slack chat.
type slackPrompter struct {
	channel *Channel
	chatID  string
}

// Ask presents options as Block Kit buttons and waits for user selection.
func (p *slackPrompter) Ask(ctx context.Context, question string, options []interact.Option) (string, error) {
	meta := p.channel.getChatMeta(p.chatID)
	if meta == nil {
		return "", fmt.Errorf("no chat context for %s", p.chatID)
	}

	promptID := "prompt:" + generateNonce()

	var buttons []slackapi.BlockElement
	for _, opt := range options {
		actionID := promptID + ":" + opt.ID
		btn := slackapi.NewButtonBlockElement(actionID, opt.ID,
			slackapi.NewTextBlockObject("plain_text", opt.Label, false, false))
		buttons = append(buttons, btn)
	}

	opts := []slackapi.MsgOption{
		slackapi.MsgOptionBlocks(
			slackapi.NewSectionBlock(
				slackapi.NewTextBlockObject("mrkdwn", question, false, false),
				nil, nil,
			),
			slackapi.NewActionBlock(promptID, buttons...),
		),
	}

	if meta.threadTS != "" {
		opts = append(opts, slackapi.MsgOptionTS(meta.threadTS))
	}

	_, _, err := p.channel.client.PostMessage(meta.channelID, opts...)
	if err != nil {
		return "", fmt.Errorf("posting prompt: %w", err)
	}

	replyCh := p.channel.prompts.register(promptID)

	select {
	case answer := <-replyCh:
		return answer, nil
	case <-ctx.Done():
		// Clean up leaked entry.
		p.channel.prompts.mu.Lock()
		delete(p.channel.prompts.pending, promptID)
		p.channel.prompts.mu.Unlock()
		return "", ctx.Err()
	}
}

// handlePromptCallback processes a Block Kit button click for interactive prompts.
// Returns true if the action was handled.
func (c *Channel) handlePromptCallback(actionID, value string) bool {
	// Extract base promptID: "prompt:<nonce>:<optionID>" → "prompt:<nonce>"
	// The promptState key is "prompt:<nonce>".
	parts := splitActionID(actionID)
	if len(parts) < 2 || parts[0] != "prompt" {
		return false
	}
	baseID := parts[0] + ":" + parts[1]
	return c.prompts.resolve(baseID, value)
}

// splitActionID splits "a:b:c" into ["a","b","c"].
func splitActionID(id string) []string {
	var parts []string
	start := 0
	for i := range id {
		if id[i] == ':' {
			parts = append(parts, id[start:i])
			start = i + 1
		}
	}
	parts = append(parts, id[start:])
	return parts
}

// PrompterFor returns a PromptAsker if the chatID belongs to this Slack channel.
func (c *Channel) PrompterFor(chatID string) interact.PromptAsker {
	if c.getChatMeta(chatID) == nil {
		return nil
	}
	return &slackPrompter{channel: c, chatID: chatID}
}

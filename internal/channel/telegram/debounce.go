package telegram

import (
	"strings"
	"sync"
	"time"

	"github.com/iulita-ai/iulita/internal/channel"
)

// debouncer buffers rapid messages from the same chat and merges them before processing.
type debouncer struct {
	mu      sync.Mutex
	buffers map[string]*chatBuffer
	window  time.Duration
	handler func(channel.IncomingMessage) // called with merged message
}

type chatBuffer struct {
	messages []channel.IncomingMessage
	timer    *time.Timer
}

func newDebouncer(window time.Duration, handler func(channel.IncomingMessage)) *debouncer {
	if window <= 0 {
		window = time.Duration(0)
	}
	return &debouncer{
		buffers: make(map[string]*chatBuffer),
		window:  window,
		handler: handler,
	}
}

// add buffers a message. If window is 0, calls handler immediately in a goroutine
// to avoid blocking the Start() loop (which must remain free to process CallbackQuery
// events for interactive prompts).
func (d *debouncer) add(msg channel.IncomingMessage) {
	if d.window <= 0 {
		go d.handler(msg)
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	buf, ok := d.buffers[msg.ChatID]
	if !ok {
		buf = &chatBuffer{}
		buf.timer = time.AfterFunc(d.window, func() {
			d.flush(msg.ChatID)
		})
		d.buffers[msg.ChatID] = buf
	} else {
		buf.timer.Reset(d.window)
	}
	buf.messages = append(buf.messages, msg)
}

func (d *debouncer) flush(chatID string) {
	d.mu.Lock()
	buf, ok := d.buffers[chatID]
	if !ok {
		d.mu.Unlock()
		return
	}
	delete(d.buffers, chatID)
	d.mu.Unlock()

	merged := mergeMessages(buf.messages)
	d.handler(merged)
}

// flushAll flushes all pending debounced messages immediately.
// Called during shutdown to avoid losing buffered messages.
func (d *debouncer) flushAll() {
	d.mu.Lock()
	ids := make([]string, 0, len(d.buffers))
	for id, buf := range d.buffers {
		ids = append(ids, id)
		buf.timer.Stop()
	}
	d.mu.Unlock()

	for _, id := range ids {
		d.flush(id)
	}
}

// mergeMessages combines multiple rapid messages into one.
func mergeMessages(msgs []channel.IncomingMessage) channel.IncomingMessage {
	if len(msgs) == 1 {
		return msgs[0]
	}

	merged := channel.IncomingMessage{
		ChatID:            msgs[0].ChatID,
		UserID:            msgs[0].UserID,
		ChannelInstanceID: msgs[0].ChannelInstanceID,
		UserName:          msgs[0].UserName,
		LanguageCode:      msgs[0].LanguageCode,
		MessageID:         msgs[0].MessageID,
	}

	var texts []string
	for _, m := range msgs {
		if m.Text != "" {
			texts = append(texts, m.Text)
		}
		merged.Images = append(merged.Images, m.Images...)
		merged.Documents = append(merged.Documents, m.Documents...)
	}

	merged.Text = strings.Join(texts, "\n")
	return merged
}

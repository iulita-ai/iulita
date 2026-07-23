package slack

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
	handler func(channel.IncomingMessage)
	timerWG sync.WaitGroup // tracks in-flight timer callbacks
}

type chatBuffer struct {
	messages []channel.IncomingMessage
	timer    *time.Timer
}

func newDebouncer(window time.Duration, handler func(channel.IncomingMessage)) *debouncer {
	if window <= 0 {
		window = 0
	}
	return &debouncer{
		buffers: make(map[string]*chatBuffer),
		window:  window,
		handler: handler,
	}
}

// add buffers a message. If window is 0, calls handler immediately.
// The handler is responsible for launching goroutines if needed.
func (d *debouncer) add(msg channel.IncomingMessage) {
	if d.window <= 0 {
		d.handler(msg)
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	buf, ok := d.buffers[msg.ChatID]
	if !ok {
		buf = &chatBuffer{}
		d.timerWG.Add(1)
		buf.timer = time.AfterFunc(d.window, func() {
			defer d.timerWG.Done()
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

// flushAll flushes all pending debounced messages immediately and waits for
// any in-flight timer callbacks so that callers can safely assume no more
// handler invocations will occur after it returns.
func (d *debouncer) flushAll() {
	d.mu.Lock()
	ids := make([]string, 0, len(d.buffers))
	stopped := make(map[string]bool, len(d.buffers))
	for id, buf := range d.buffers {
		ids = append(ids, id)
		// Stop returns true only if the timer was stopped before firing — in
		// that case the AfterFunc never runs, so its timerWG.Done() must be
		// called here to balance the Add() done in add().
		stopped[id] = buf.timer.Stop()
	}
	d.mu.Unlock()

	for _, id := range ids {
		if stopped[id] {
			d.timerWG.Done()
		}
		d.flush(id)
	}

	// Wait for any AfterFunc goroutines that already fired and are running
	// concurrently. They invoke handler synchronously, so once Wait returns
	// no further handler calls can happen.
	d.timerWG.Wait()
}

// mergeMessages combines multiple rapid messages into one.
func mergeMessages(msgs []channel.IncomingMessage) channel.IncomingMessage {
	if len(msgs) == 1 {
		return msgs[0]
	}

	merged := channel.IncomingMessage{
		ChatID:            msgs[0].ChatID,
		UserID:            msgs[0].UserID,
		ResolvedUserID:    msgs[0].ResolvedUserID,
		ChannelInstanceID: msgs[0].ChannelInstanceID,
		UserName:          msgs[0].UserName,
		LanguageCode:      msgs[0].LanguageCode,
		Locale:            msgs[0].Locale,
		MessageID:         msgs[0].MessageID,
		Caps:              msgs[0].Caps,
	}

	texts := make([]string, 0, len(msgs))
	for i := range msgs {
		m := &msgs[i]
		if m.Text != "" {
			texts = append(texts, m.Text)
		}
		merged.Images = append(merged.Images, m.Images...)
		merged.Documents = append(merged.Documents, m.Documents...)
		merged.Audio = append(merged.Audio, m.Audio...)
	}

	merged.Text = strings.Join(texts, "\n")
	return merged
}

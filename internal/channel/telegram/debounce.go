package telegram

import (
	"strings"
	"sync"
	"time"

	"github.com/iulita-ai/iulita/internal/channel"
)

// debouncer buffers rapid messages from the same chat and merges them before processing.
type debouncer struct {
	mu        sync.Mutex
	buffers   map[string]*chatBuffer
	window    time.Duration
	handler   func(channel.IncomingMessage) // called with merged message
	tails     map[string]chan struct{}
	wg        sync.WaitGroup
	onDiscard func(channel.IncomingMessage)
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
		tails:   make(map[string]chan struct{}),
	}
}

// add buffers a message. If window is 0, calls handler immediately in a goroutine
// to avoid blocking the Start() loop (which must remain free to process CallbackQuery
// events for interactive prompts).
func (d *debouncer) add(msg channel.IncomingMessage) {
	if d.window <= 0 {
		d.mu.Lock()
		d.enqueueLocked(msg.ChatID, func() { d.handler(msg) })
		d.mu.Unlock()
		return
	}

	key := msg.ChatID
	if msg.ChannelType == "telegram" && !msg.ProvenPrivateRoute {
		key += "\x00" + msg.UserID
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	buf, ok := d.buffers[key]
	if !ok {
		buf = &chatBuffer{}
		buf.timer = time.AfterFunc(d.window, func() {
			d.flush(key)
		})
		d.buffers[key] = buf
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
	merged := mergeMessages(buf.messages)
	d.enqueueLocked(merged.ChatID, func() { d.handler(merged) })
	d.mu.Unlock()
}

// control orders a context command after already accepted turns without
// blocking the Telegram dispatcher (which must keep serving approval clicks).
// Clear discards an unprocessed debounce burst; compact processes it first.
func (d *debouncer) control(chatID string, discard bool, fn func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if buf := d.buffers[chatID]; buf != nil {
		buf.timer.Stop()
		delete(d.buffers, chatID)
		if discard && d.onDiscard != nil {
			merged := mergeMessages(buf.messages)
			d.enqueueLocked(chatID, func() { d.onDiscard(merged) })
		} else if !discard {
			merged := mergeMessages(buf.messages)
			d.enqueueLocked(chatID, func() { d.handler(merged) })
		}
	}
	d.enqueueLocked(chatID, fn)
}

func (d *debouncer) enqueueLocked(key string, fn func()) {
	previous := d.tails[key]
	done := make(chan struct{})
	d.tails[key] = done
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		if previous != nil {
			<-previous
		}
		defer func() {
			close(done)
			d.mu.Lock()
			if d.tails[key] == done {
				delete(d.tails, key)
			}
			d.mu.Unlock()
		}()
		fn()
	}()
}

func (d *debouncer) wait() { d.wg.Wait() }

// flushAll flushes all pending debounced messages immediately.
// Called during shutdown to avoid losing buffered messages.
func (d *debouncer) flushAll() {
	for {
		d.mu.Lock()
		for id, buf := range d.buffers {
			buf.timer.Stop()
			delete(d.buffers, id)
			merged := mergeMessages(buf.messages)
			d.enqueueLocked(merged.ChatID, func() { d.handler(merged) })
		}
		d.mu.Unlock()
		d.wait()
		// A queued topic preparation may have enqueued a new debounced turn.
		d.mu.Lock()
		empty := len(d.buffers) == 0 && len(d.tails) == 0
		d.mu.Unlock()
		if empty {
			return
		}
	}
}

// mergeMessages combines multiple rapid messages into one.
func mergeMessages(msgs []channel.IncomingMessage) channel.IncomingMessage {
	if len(msgs) == 1 {
		return msgs[0]
	}

	// Identity fields take the first message's values (consistent with the
	// existing msgs[0].UserID behavior). Locale, Caps and ResolvedUserID used
	// to be dropped here, which mis-scoped facts and degraded response
	// language on merged bursts.
	merged := channel.IncomingMessage{
		ChannelType:        msgs[0].ChannelType,
		ChatID:             msgs[0].ChatID,
		UserID:             msgs[0].UserID,
		ChannelInstanceID:  msgs[0].ChannelInstanceID,
		UserName:           msgs[0].UserName,
		LanguageCode:       msgs[0].LanguageCode,
		Locale:             msgs[0].Locale,
		Caps:               msgs[0].Caps,
		ResolvedUserID:     msgs[0].ResolvedUserID,
		MessageID:          msgs[0].MessageID,
		ProvenPrivateRoute: msgs[0].ProvenPrivateRoute,
		Quote:              msgs[0].Quote,
	}

	var texts []string
	for _, m := range msgs {
		if len(m.RelatedMessageIDs) > 0 {
			merged.RelatedMessageIDs = append(merged.RelatedMessageIDs, m.RelatedMessageIDs...)
		} else if m.MessageID > 0 {
			merged.RelatedMessageIDs = append(merged.RelatedMessageIDs, m.MessageID)
		}
		if !m.ProvenPrivateRoute || m.ChannelType != merged.ChannelType || m.ChatID != merged.ChatID || m.UserID != merged.UserID || m.ResolvedUserID != merged.ResolvedUserID || m.ChannelInstanceID != merged.ChannelInstanceID {
			merged.ProvenPrivateRoute = false
		}
		if m.Text != "" {
			texts = append(texts, m.Text)
		}
		merged.Images = append(merged.Images, m.Images...)
		merged.Documents = append(merged.Documents, m.Documents...)
		merged.Audio = append(merged.Audio, m.Audio...)
		merged.Locations = append(merged.Locations, m.Locations...)
	}

	merged.Text = strings.Join(texts, "\n")
	return merged
}

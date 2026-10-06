// Package webchat implements a web-based chat channel using WebSocket.
package webchat

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"time"

	"github.com/gofiber/contrib/websocket"
	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/bookmark"
	"github.com/iulita-ai/iulita/internal/channel"
)

// wsIncomingMessage is the JSON structure received from the browser.
type wsIncomingMessage struct {
	Text              string `json:"text"`
	ChatID            string `json:"chat_id"`                       // optional override; defaults to user ID
	PromptID          string `json:"prompt_id,omitempty"`           // set when replying to a prompt
	PromptAnswer      string `json:"prompt_answer,omitempty"`       // the selected option ID or typed text
	RememberMessageID string `json:"remember_message_id,omitempty"` // non-empty = bookmark this message
}

// rememberAck is the bookmark confirmation sent to the browser.
type rememberAck struct {
	MessageID string `json:"message_id"`
	FactID    int64  `json:"fact_id,omitempty"`
	Status    string `json:"status"` // "saved", "error", "expired"
	Error     string `json:"error,omitempty"`
}

// wsOutgoingMessage is the JSON structure sent to the browser.
type wsOutgoingMessage struct {
	Type        string            `json:"type"` // "message", "stream_edit", "stream_done", "status", "prompt", "remember_ack"
	Text        string            `json:"text"`
	MessageID   string            `json:"message_id,omitempty"`
	PromptID    string            `json:"prompt_id,omitempty"`
	Options     []wsPromptOption  `json:"options,omitempty"`
	Timestamp   string            `json:"timestamp"`
	Status      string            `json:"status,omitempty"`      // for type="status": "processing", "skill_start", "skill_done", "stream_start", "error"
	SkillName   string            `json:"skill_name,omitempty"`  // skill being executed
	Success     bool              `json:"success,omitempty"`     // skill result
	DurationMs  int64             `json:"duration_ms,omitempty"` // skill duration
	Error       string            `json:"error,omitempty"`       // error message
	Data        map[string]string `json:"data,omitempty"`        // extra payload (e.g. locale for locale_changed)
	RememberAck *rememberAck      `json:"remember_ack,omitempty"`
}

// Channel implements channel.InputChannel and channel.StreamingSender for web chat.
type Channel struct {
	instanceID  string
	logger      *zap.Logger
	bookmarkSvc bookmark.Service // nil = no bookmark feature

	mu             sync.RWMutex
	clients        map[string]*websocket.Conn // chatID → connection
	writeMu        map[string]*sync.Mutex     // chatID → per-connection write mutex
	handler        channel.MessageHandler
	pendingPrompts sync.Map // key: "chatID:promptID" → chan string
	msgCache       sync.Map // messageID (string) → content (string) for bookmark lookups
}

// New creates a new web chat channel.
func New(logger *zap.Logger) *Channel {
	return &Channel{
		clients: make(map[string]*websocket.Conn),
		writeMu: make(map[string]*sync.Mutex),
		logger:  logger,
	}
}

// SetInstanceID sets the channel instance ID.
func (c *Channel) SetInstanceID(id string) {
	c.instanceID = id
}

// SetBookmarkService attaches a bookmark service for the "remember" button feature.
func (c *Channel) SetBookmarkService(svc bookmark.Service) {
	c.bookmarkSvc = svc
}

// FiberHandler returns the WebSocket handler for mounting on a Fiber app.
func (c *Channel) FiberHandler() fiber.Handler {
	return websocket.New(c.handleConnection)
}

// FiberUpgradeCheck returns middleware that validates WebSocket upgrade requests.
func (c *Channel) FiberUpgradeCheck() fiber.Handler {
	return func(fc *fiber.Ctx) error {
		if websocket.IsWebSocketUpgrade(fc) {
			return fc.Next()
		}
		return fiber.ErrUpgradeRequired
	}
}

func (c *Channel) closeConnection(conn *websocket.Conn) {
	if err := conn.Close(); err != nil {
		c.logger.Debug("webchat connection close failed", zap.Error(err))
	}
}

func (c *Channel) handleConnection(conn *websocket.Conn) {
	claims, ok := conn.Locals(auth.ContextKeyUser).(*auth.Claims)
	if !ok || claims.UserID == "" || claims.ExpiresAt == nil {
		c.closeConnection(conn)
		return
	}
	// In-flight messages and stream callbacks can outlive this handler. Keep a
	// private wrapper so Fiber cannot recycle their connection into another chat.
	conn = &websocket.Conn{Conn: conn.Conn}
	if err := conn.SetReadDeadline(claims.ExpiresAt.Time); err != nil {
		c.closeConnection(conn)
		return
	}
	ctx, cancel := context.WithDeadline(context.Background(), claims.ExpiresAt.Time)
	defer cancel()
	// Fiber returns its connection wrapper to a pool as soon as this handler
	// exits. The asynchronous cancellation callback must hold the raw socket.
	rawSocket := conn.Conn
	stopClose := context.AfterFunc(ctx, func() {
		if err := rawSocket.Close(); err != nil {
			c.logger.Debug("webchat expired connection close failed", zap.Error(err))
		}
	})
	defer stopClose()
	if ctx.Err() != nil {
		c.closeConnection(conn)
		return
	}
	userID, username := claims.UserID, claims.Username
	chatID := "web:" + userID
	writeMu := &sync.Mutex{}

	c.mu.Lock()
	previous := c.clients[chatID]
	c.clients[chatID] = conn
	c.writeMu[chatID] = writeMu
	c.mu.Unlock()
	if previous != nil && previous != conn {
		// fasthttp's hijacked Close may be deferred until the handler returns.
		// Interrupt its reader now so replacement also cancels pending work.
		if err := previous.SetReadDeadline(time.Now()); err != nil {
			c.logger.Debug("webchat replaced connection deadline failed", zap.Error(err))
		}
		c.closeConnection(previous)
	}

	c.logger.Info("webchat client connected",
		zap.String("user_id", userID),
		zap.String("chat_id", chatID))

	defer func() {
		cancel()
		c.mu.Lock()
		if c.clients[chatID] == conn {
			delete(c.clients, chatID)
			delete(c.writeMu, chatID)
		}
		c.mu.Unlock()
		c.closeConnection(conn)
		// The upgrader recycles its write buffer after this handler returns.
		// Closing unblocks the current writer; queued writers recheck membership.
		writeMu.Lock()
		defer writeMu.Unlock()
		c.logger.Info("webchat client disconnected", zap.String("chat_id", chatID))
	}()

	for {
		_, msgBytes, err := conn.ReadMessage()
		if err != nil || ctx.Err() != nil {
			break
		}
		c.mu.RLock()
		active := c.clients[chatID] == conn
		c.mu.RUnlock()
		if !active {
			break
		}

		var incoming wsIncomingMessage
		if err := json.Unmarshal(msgBytes, &incoming); err != nil {
			c.logger.Debug("webchat: invalid message", zap.Error(err))
			continue
		}
		if ctx.Err() != nil {
			break
		}

		// Handle bookmark requests.
		if incoming.RememberMessageID != "" && c.bookmarkSvc != nil {
			if ctx.Err() == nil {
				go c.handleRemember(ctx, conn, chatID, userID, incoming.RememberMessageID)
			}
			continue
		}

		// Handle prompt responses.
		if incoming.PromptID != "" {
			key := chatID + ":" + incoming.PromptID
			if v, ok := c.pendingPrompts.Load(key); ok {
				if ch, ok := v.(chan string); ok {
					answer := incoming.PromptAnswer
					if answer == "" {
						answer = incoming.Text
					}
					select {
					case ch <- answer:
					default:
					}
				}
			}
			continue
		}

		if incoming.Text == "" {
			continue
		}

		// Web chat users are already authenticated via JWT — use the user_id directly.
		resolvedUserID := userID

		msg := channel.IncomingMessage{
			ChatID:            chatID,
			UserID:            userID,
			ResolvedUserID:    resolvedUserID,
			ChannelInstanceID: c.instanceID,
			UserName:          username,
			Text:              incoming.Text,
			Caps:              channel.CapStreaming | channel.CapHTML | channel.CapButtons,
		}

		c.mu.RLock()
		handler := c.handler
		c.mu.RUnlock()
		if handler != nil && ctx.Err() == nil {
			go c.processMessage(ctx, conn, msg, handler)
		}
	}
}

func (c *Channel) processMessage(ctx context.Context, conn *websocket.Conn, msg channel.IncomingMessage, handler channel.MessageHandler) {
	if ctx.Err() != nil {
		return
	}
	response, err := handler(ctx, msg)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		c.logger.Error("webchat handler error", zap.Error(err), zap.String("chat_id", msg.ChatID))
		response = "Sorry, something went wrong."
	}

	msgIDStr := strconv.FormatInt(time.Now().UnixNano(), 10)
	c.sendToConn(conn, wsOutgoingMessage{
		Type:      "message",
		Text:      response,
		MessageID: msgIDStr,
		Timestamp: time.Now().Format(time.RFC3339),
	})
	c.cacheMessage(msgIDStr, msg.ChatID, response)
}

func (c *Channel) sendToConn(conn *websocket.Conn, msg wsOutgoingMessage) {
	data, err := json.Marshal(msg)
	if err != nil {
		c.logger.Error("webchat: failed to marshal message", zap.Error(err))
		return
	}
	// Find the per-connection write mutex to serialize writes.
	c.mu.RLock()
	var wmu *sync.Mutex
	var chatID string
	for cid, cn := range c.clients {
		if cn == conn {
			wmu = c.writeMu[cid]
			chatID = cid
			break
		}
	}
	c.mu.RUnlock()

	if wmu == nil {
		return // The connection was replaced or disconnected.
	}
	wmu.Lock()
	defer wmu.Unlock()
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.clients[chatID] != conn || c.writeMu[chatID] != wmu {
		return
	}
	if err := conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		c.logger.Debug("webchat write deadline failed", zap.Error(err))
		return
	}
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		c.logger.Debug("webchat: failed to write message", zap.Error(err))
	}
}

// cachedMsg stores message content alongside its owner for security validation.
type cachedMsg struct {
	content string
	chatID  string // owner's chat ID (e.g. "web:user-uuid")
}

// handleRemember processes a bookmark request from the frontend.
func (c *Channel) handleRemember(ctx context.Context, conn *websocket.Conn, chatID, userID, messageID string) {
	if ctx.Err() != nil {
		return
	}
	raw, ok := c.msgCache.LoadAndDelete(messageID)
	if !ok {
		c.sendToConn(conn, wsOutgoingMessage{
			Type:        "remember_ack",
			RememberAck: &rememberAck{MessageID: messageID, Status: "expired"},
		})
		return
	}

	cached, ok := raw.(cachedMsg)
	if !ok {
		return
	}
	// Verify the requesting user owns this message.
	if cached.chatID != chatID {
		c.logger.Warn("bookmark ownership mismatch",
			zap.String("requested_by", chatID),
			zap.String("owned_by", cached.chatID))
		c.sendToConn(conn, wsOutgoingMessage{
			Type:        "remember_ack",
			RememberAck: &rememberAck{MessageID: messageID, Status: "expired"},
		})
		return
	}

	if ctx.Err() != nil {
		return
	}
	factID, err := c.bookmarkSvc.Save(ctx, chatID, userID, cached.content)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		c.logger.Error("bookmark save failed", zap.Error(err), zap.String("chat_id", chatID))
		c.sendToConn(conn, wsOutgoingMessage{
			Type:        "remember_ack",
			RememberAck: &rememberAck{MessageID: messageID, Status: "error", Error: err.Error()},
		})
		return
	}

	c.sendToConn(conn, wsOutgoingMessage{
		Type:        "remember_ack",
		RememberAck: &rememberAck{MessageID: messageID, FactID: factID, Status: "saved"},
	})
}

// cacheMessage stores message content for bookmark lookups with a 10-minute TTL.
func (c *Channel) cacheMessage(messageID, chatID, content string) {
	if messageID == "" || content == "" {
		return
	}
	c.msgCache.Store(messageID, cachedMsg{content: content, chatID: chatID})
	time.AfterFunc(10*time.Minute, func() {
		c.msgCache.Delete(messageID)
	})
}

// Start implements channel.InputChannel. It stores the handler and blocks until ctx is done.
func (c *Channel) Start(ctx context.Context, handler channel.MessageHandler) error {
	c.mu.Lock()
	c.handler = handler
	c.mu.Unlock()
	c.logger.Info("webchat channel started", zap.String("instance_id", c.instanceID))
	<-ctx.Done()
	c.logger.Info("webchat channel stopped")
	return ctx.Err()
}

// SendMessage sends a message to a web chat client.
func (c *Channel) SendMessage(_ context.Context, chatID string, text string) error {
	c.mu.RLock()
	conn, ok := c.clients[chatID]
	c.mu.RUnlock()

	if !ok {
		return nil // client not connected, skip
	}

	c.sendToConn(conn, wsOutgoingMessage{
		Type:      "message",
		Text:      text,
		Timestamp: time.Now().Format(time.RFC3339),
	})
	return nil
}

// NotifyStatus sends a processing status update to a specific chat client.
func (c *Channel) NotifyStatus(_ context.Context, chatID string, event channel.StatusEvent) error {
	c.mu.RLock()
	conn, ok := c.clients[chatID]
	c.mu.RUnlock()

	if !ok {
		return nil // client not connected
	}

	c.sendToConn(conn, wsOutgoingMessage{
		Type:       "status",
		Status:     event.Type,
		SkillName:  event.SkillName,
		Success:    event.Success,
		DurationMs: event.DurationMs,
		Error:      event.Error,
		Data:       event.Data,
		Timestamp:  time.Now().Format(time.RFC3339),
	})
	return nil
}

// StartStream sends an initial placeholder and returns edit/done functions.
func (c *Channel) StartStream(_ context.Context, chatID string, _ int) (func(string), func(string), error) {
	c.mu.RLock()
	conn, ok := c.clients[chatID]
	c.mu.RUnlock()

	if !ok {
		return nil, nil, nil // no connected client
	}

	msgIDStr := strconv.FormatInt(time.Now().UnixNano(), 10)

	editFn := func(text string) {
		c.sendToConn(conn, wsOutgoingMessage{
			Type:      "stream_edit",
			Text:      text,
			MessageID: msgIDStr,
			Timestamp: time.Now().Format(time.RFC3339),
		})
	}

	doneFn := func(text string) {
		c.sendToConn(conn, wsOutgoingMessage{
			Type:      "stream_done",
			Text:      text,
			MessageID: msgIDStr,
			Timestamp: time.Now().Format(time.RFC3339),
		})
		c.cacheMessage(msgIDStr, chatID, text)
	}

	return editFn, doneFn, nil
}

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

	"github.com/iulita-ai/iulita/internal/channel"
)

// wsIncomingMessage is the JSON structure received from the browser.
type wsIncomingMessage struct {
	Text         string `json:"text"`
	ChatID       string `json:"chat_id"`                 // optional override; defaults to user ID
	PromptID     string `json:"prompt_id,omitempty"`     // set when replying to a prompt
	PromptAnswer string `json:"prompt_answer,omitempty"` // the selected option ID or typed text
}

// wsOutgoingMessage is the JSON structure sent to the browser.
type wsOutgoingMessage struct {
	Type       string            `json:"type"` // "message", "stream_edit", "stream_done", "status", "prompt"
	Text       string            `json:"text"`
	MessageID  string            `json:"message_id,omitempty"`
	PromptID   string            `json:"prompt_id,omitempty"`
	Options    []wsPromptOption  `json:"options,omitempty"`
	Timestamp  string            `json:"timestamp"`
	Status     string            `json:"status,omitempty"`      // for type="status": "processing", "skill_start", "skill_done", "stream_start", "error"
	SkillName  string            `json:"skill_name,omitempty"`  // skill being executed
	Success    bool              `json:"success,omitempty"`     // skill result
	DurationMs int64             `json:"duration_ms,omitempty"` // skill duration
	Error      string            `json:"error,omitempty"`       // error message
	Data       map[string]string `json:"data,omitempty"`        // extra payload (e.g. locale for locale_changed)
}

// Channel implements channel.InputChannel and channel.StreamingSender for web chat.
type Channel struct {
	instanceID string
	logger     *zap.Logger

	mu             sync.RWMutex
	clients        map[string]*websocket.Conn // chatID → connection
	writeMu        map[string]*sync.Mutex     // chatID → per-connection write mutex
	handler        channel.MessageHandler
	pendingPrompts sync.Map // key: "chatID:promptID" → chan string
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

func (c *Channel) handleConnection(conn *websocket.Conn) {
	// Extract user info from query params (set by frontend from JWT).
	userID := conn.Query("user_id", "")
	username := conn.Query("username", "")
	chatID := conn.Query("chat_id", "")

	if userID == "" {
		c.logger.Warn("webchat: connection without user_id")
		conn.Close()
		return
	}
	if chatID == "" {
		chatID = "web:" + userID
	}

	c.mu.Lock()
	c.clients[chatID] = conn
	c.writeMu[chatID] = &sync.Mutex{}
	c.mu.Unlock()

	c.logger.Info("webchat client connected",
		zap.String("user_id", userID),
		zap.String("chat_id", chatID))

	defer func() {
		c.mu.Lock()
		delete(c.clients, chatID)
		delete(c.writeMu, chatID)
		c.mu.Unlock()
		conn.Close()
		c.logger.Info("webchat client disconnected", zap.String("chat_id", chatID))
	}()

	for {
		_, msgBytes, err := conn.ReadMessage()
		if err != nil {
			break
		}

		var incoming wsIncomingMessage
		if err := json.Unmarshal(msgBytes, &incoming); err != nil {
			c.logger.Debug("webchat: invalid message", zap.Error(err))
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

		if c.handler != nil {
			go c.processMessage(conn, msg)
		}
	}
}

func (c *Channel) processMessage(conn *websocket.Conn, msg channel.IncomingMessage) {
	response, err := c.handler(context.Background(), msg)
	if err != nil {
		c.logger.Error("webchat handler error", zap.Error(err), zap.String("chat_id", msg.ChatID))
		response = "Sorry, something went wrong."
	}

	c.sendToConn(conn, wsOutgoingMessage{
		Type:      "message",
		Text:      response,
		Timestamp: time.Now().Format(time.RFC3339),
	})
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
	for cid, cn := range c.clients {
		if cn == conn {
			wmu = c.writeMu[cid]
			break
		}
	}
	c.mu.RUnlock()

	if wmu != nil {
		wmu.Lock()
		defer wmu.Unlock()
	}
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		c.logger.Debug("webchat: failed to write message", zap.Error(err))
	}
}

// Start implements channel.InputChannel. It stores the handler and blocks until ctx is done.
func (c *Channel) Start(ctx context.Context, handler channel.MessageHandler) error {
	c.handler = handler
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
	}

	return editFn, doneFn, nil
}

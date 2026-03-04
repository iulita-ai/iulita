package dashboard

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/gofiber/contrib/websocket"
	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/eventbus"
)

// WSMessage is the JSON envelope for WebSocket messages.
type WSMessage struct {
	Type    string      `json:"type"`    // event type
	Payload interface{} `json:"payload"` // event data
}

// WSHub manages WebSocket connections and broadcasts events.
type WSHub struct {
	mu          sync.RWMutex
	connections map[*websocket.Conn]struct{}
	logger      *zap.Logger
}

// NewWSHub creates a new WebSocket hub.
func NewWSHub(logger *zap.Logger) *WSHub {
	return &WSHub{
		connections: make(map[*websocket.Conn]struct{}),
		logger:      logger,
	}
}

// Register adds a connection to the hub.
func (h *WSHub) Register(conn *websocket.Conn) {
	h.mu.Lock()
	h.connections[conn] = struct{}{}
	h.mu.Unlock()
	h.logger.Debug("websocket client connected", zap.Int("total", h.Count()))
}

// Unregister removes a connection from the hub.
func (h *WSHub) Unregister(conn *websocket.Conn) {
	h.mu.Lock()
	delete(h.connections, conn)
	h.mu.Unlock()
	h.logger.Debug("websocket client disconnected", zap.Int("total", h.Count()))
}

// Count returns the number of active connections.
func (h *WSHub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.connections)
}

// Broadcast sends a message to all connected clients.
func (h *WSHub) Broadcast(msg WSMessage) {
	data, err := json.Marshal(msg)
	if err != nil {
		h.logger.Error("failed to marshal ws message", zap.Error(err))
		return
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for conn := range h.connections {
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			h.logger.Debug("failed to write to ws client", zap.Error(err))
		}
	}
}

// HandleWebSocket is the Fiber websocket handler.
func (h *WSHub) HandleWebSocket(c *websocket.Conn) {
	h.Register(c)
	defer h.Unregister(c)
	defer c.Close()

	// Send initial connected message.
	initial := WSMessage{Type: "connected", Payload: map[string]interface{}{
		"timestamp": time.Now().Format(time.RFC3339),
	}}
	if data, err := json.Marshal(initial); err == nil {
		c.WriteMessage(websocket.TextMessage, data)
	}

	// Read loop — keep connection alive, handle pings/pongs.
	for {
		_, _, err := c.ReadMessage()
		if err != nil {
			break // client disconnected
		}
	}
}

// RegisterEventSubscribers subscribes the hub to relevant event bus events.
func (h *WSHub) RegisterEventSubscribers(bus *eventbus.Bus) {
	forward := func(eventType string) func(context.Context, eventbus.Event) error {
		return func(_ context.Context, evt eventbus.Event) error {
			h.Broadcast(WSMessage{
				Type:    eventType,
				Payload: evt.Payload,
			})
			return nil
		}
	}

	bus.SubscribeAsync(eventbus.TaskCompleted, forward(eventbus.TaskCompleted))
	bus.SubscribeAsync(eventbus.TaskFailed, forward(eventbus.TaskFailed))
	bus.SubscribeAsync(eventbus.MessageReceived, forward(eventbus.MessageReceived))
	bus.SubscribeAsync(eventbus.ResponseSent, forward(eventbus.ResponseSent))
	bus.SubscribeAsync(eventbus.FactSaved, forward(eventbus.FactSaved))
	bus.SubscribeAsync(eventbus.InsightCreated, forward(eventbus.InsightCreated))
	bus.SubscribeAsync(eventbus.ConfigChanged, forward(eventbus.ConfigChanged))
}

// SetupWebSocket adds the WebSocket upgrade middleware and handler to the Fiber app.
func SetupWebSocket(app *fiber.App, hub *WSHub) {
	app.Use("/ws", func(c *fiber.Ctx) error {
		if websocket.IsWebSocketUpgrade(c) {
			return c.Next()
		}
		return fiber.ErrUpgradeRequired
	})
	app.Get("/ws", websocket.New(hub.HandleWebSocket))
}

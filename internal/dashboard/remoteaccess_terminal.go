package dashboard

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/contrib/websocket"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/remoteaccess"
)

type terminalTicket struct {
	SecurityEpoch        uint64
	Owner                string
	Request              remoteaccess.TerminalRequest
	Expires, AuthExpires time.Time
}
type terminalTicketStore struct {
	mu      sync.Mutex
	tickets map[string]terminalTicket
}

func newTerminalTicketStore() *terminalTicketStore {
	return &terminalTicketStore{tickets: map[string]terminalTicket{}}
}
func (s *Server) remoteTerminalPreview(c *fiber.Ctx) error {
	sc, err := remoteScope(c)
	if err != nil {
		return err
	}
	var req remoteaccess.TerminalRequest
	if bodyErr := remoteBody(c, &req); bodyErr != nil {
		return bodyErr
	}
	a, err := s.remoteAccess.PreviewTerminal(sc, req)
	if err != nil {
		return remoteError(err)
	}
	return c.JSON(a)
}
func (s *Server) remoteTerminalTicket(c *fiber.Ctx) error {
	sc, err := remoteScope(c)
	if err != nil {
		return err
	}
	var req remoteaccess.TerminalRequest
	if bodyErr := remoteBody(c, &req); bodyErr != nil {
		return bodyErr
	}
	if req.ApprovalID == "" || req.ExpectedPlanHash == "" {
		return fiber.ErrBadRequest
	}
	claims := auth.GetClaims(c)
	if claims.ExpiresAt == nil {
		return fiber.ErrUnauthorized
	}
	user, lookupErr := s.store.GetUser(c.UserContext(), sc.OwnerID)
	if lookupErr != nil || user == nil || user.Disabled || user.MustChangePass {
		return fiber.ErrUnauthorized
	}
	store := s.terminalTickets
	store.mu.Lock()
	defer store.mu.Unlock()
	count := 0
	for id := range store.tickets {
		ticket := store.tickets[id]
		if !ticket.Expires.After(time.Now()) {
			delete(store.tickets, id)
		} else if ticket.Owner == sc.OwnerID {
			count++
		}
	}
	if count >= 2 || len(store.tickets) >= 200 {
		return fiber.ErrTooManyRequests
	}
	id := uuid.NewString()
	expires := time.Now().Add(30 * time.Second)
	store.tickets[id] = terminalTicket{SecurityEpoch: user.SecurityEpoch, Owner: sc.OwnerID, Request: req, Expires: expires, AuthExpires: claims.ExpiresAt.Time}
	return c.JSON(fiber.Map{"ticket": id, "expires_at": expires})
}
func (s *Server) remoteTerminalUpgrade(c *fiber.Ctx) error {
	// Tickets are single-use browser capabilities, issued only after authentication.
	// Browser Origin must match exactly; query strings never contain bearer tokens.
	origin, err := url.Parse(c.Get("Origin"))
	if err != nil || origin == nil || origin.Host == "" || !strings.EqualFold(origin.Host, c.Hostname()) || (origin.Scheme != "http" && origin.Scheme != "https") {
		return fiber.ErrForbidden
	}
	scheme := c.Protocol()
	if scheme == "ws" {
		scheme = "http"
	}
	if scheme == "wss" {
		scheme = "https"
	}
	if origin.Scheme != scheme {
		return fiber.ErrForbidden
	}
	store := s.terminalTickets
	store.mu.Lock()
	ticket, ok := store.tickets[c.Params("ticket")]
	delete(store.tickets, c.Params("ticket"))
	store.mu.Unlock()
	if !ok || !ticket.Expires.After(time.Now()) || !ticket.AuthExpires.After(time.Now()) {
		return fiber.ErrUnauthorized
	}
	user, err := s.store.GetUser(c.UserContext(), ticket.Owner)
	if err != nil || user == nil || user.MustChangePass || user.Disabled || user.SecurityEpoch != ticket.SecurityEpoch {
		return fiber.ErrUnauthorized
	}
	c.Locals("remote_terminal_ticket", ticket)
	return websocket.New(s.remoteTerminalSocket)(c)
}

type browserTerminalFrame struct {
	Kind        string `json:"kind"`
	Data        []byte `json:"data,omitempty"`
	Rows        int    `json:"rows,omitempty"`
	Cols        int    `json:"cols,omitempty"`
	ExecutionID string `json:"execution_id,omitempty"`
}

func (s *Server) remoteTerminalSocket(conn *websocket.Conn) {
	ticket, ok := conn.Locals("remote_terminal_ticket").(terminalTicket)
	if !ok {
		closeTerminalSocket(conn)
		return
	}
	conn = &websocket.Conn{Conn: conn.Conn}
	ctx, cancel := context.WithDeadline(context.Background(), ticket.AuthExpires)
	defer cancel()
	defer closeTerminalSocket(conn)
	go func() {
		tick := time.NewTicker(5 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				user, lookupErr := s.store.GetUser(ctx, ticket.Owner)
				if lookupErr != nil || user == nil || user.Disabled || user.MustChangePass || user.SecurityEpoch != ticket.SecurityEpoch {
					cancel()
					return
				}
			}
		}
	}()
	meta, session, err := s.remoteAccess.OpenTerminal(ctx, remoteaccess.Scope{OwnerID: ticket.Owner, CallerID: "dashboard"}, ticket.Request)
	if err != nil {
		return
	}
	defer closeTerminalSession(session)
	conn.SetReadLimit(8192)
	if err = conn.SetReadDeadline(ticket.AuthExpires); err != nil {
		return
	}
	if err = conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return
	}
	if err = conn.WriteJSON(browserTerminalFrame{Kind: "ready", ExecutionID: meta.ExecutionID}); err != nil {
		return
	}
	go func() { <-ctx.Done(); closeTerminalSession(session); closeTerminalSocket(conn) }()
	go func() {
		defer cancel()
		buffer := make([]byte, 4096)
		for {
			n, readErr := session.Read(buffer)
			if n > 0 {
				if deadlineErr := conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); deadlineErr != nil {
					return
				}
				if writeErr := conn.WriteJSON(browserTerminalFrame{Kind: "output", Data: buffer[:n]}); writeErr != nil {
					return
				}
			}
			if readErr != nil {
				return
			}
		}
	}()
	window := time.Now()
	frames := 0
	for {
		_, data, readErr := conn.ReadMessage()
		if readErr != nil {
			return
		}
		if time.Since(window) >= time.Second {
			window = time.Now()
			frames = 0
		}
		frames++
		if frames > 128 {
			return
		}
		var frame browserTerminalFrame
		if json.Unmarshal(data, &frame) != nil {
			return
		}
		switch frame.Kind {
		case "input":
			if len(frame.Data) > 4096 {
				return
			}
			if _, err = session.Write(frame.Data); err != nil {
				return
			}
		case "resize":
			if err = session.Resize(frame.Rows, frame.Cols); err != nil {
				return
			}
		case "close":
			return
		default:
			return
		}
	}
}
func closeTerminalSocket(c *websocket.Conn) {
	if err := c.Close(); err != nil {
		return
	}
}
func closeTerminalSession(c remoteaccess.PTYSession) {
	if err := c.Close(); err != nil {
		return
	}
}

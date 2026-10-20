package remoteaccess

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

type terminalEnvelope struct {
	ID       string
	IssuedAt time.Time
	Request  PTYRequest
}
type terminalFrame struct {
	Kind      string `json:"kind"`
	Error     string `json:"error,omitempty"`
	Data      []byte `json:"data,omitempty"`
	Rows      int    `json:"rows,omitempty"`
	Cols      int    `json:"cols,omitempty"`
	Signature string `json:"signature,omitempty"`
}
type rpcPTY struct {
	conn      *websocket.Conn
	mu        sync.Mutex
	remaining []byte
	cancel    context.CancelFunc
	once      sync.Once
	done      chan struct{}
	err       error
}

// OpenPTY authenticates one terminal over the private worker channel.
func (t *RPCTransport) OpenPTY(parent context.Context, req PTYRequest) (PTYSession, error) {
	if err := validatePTY(&req); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, req.Timeout)
	envelope := terminalEnvelope{ID: uuid.NewString(), IssuedAt: time.Now().UTC(), Request: req}
	data, err := json.Marshal(envelope)
	if err != nil || len(data) > workerMaxBody {
		cancel()
		return nil, errors.New("terminal request too large")
	}
	transport, ok := t.client.Transport.(*http.Transport)
	if !ok {
		cancel()
		return nil, errors.New("invalid worker transport")
	}
	dialer := websocket.Dialer{NetDialContext: transport.DialContext, TLSClientConfig: transport.TLSClientConfig, HandshakeTimeout: 10 * time.Second}
	endpoint := strings.TrimSuffix(t.endpoint, "/execute") + "/terminal"
	endpoint = strings.Replace(endpoint, "https://", "wss://", 1)
	endpoint = strings.Replace(endpoint, "http://", "ws://", 1)
	conn, response, err := dialer.DialContext(ctx, endpoint, nil)
	if response != nil && response.Body != nil {
		if closeErr := response.Body.Close(); closeErr != nil {
			if conn != nil {
				closeTransport(conn)
			}
			cancel()
			return nil, errors.New("terminal handshake response unavailable")
		}
	}
	if err != nil {
		cancel()
		return nil, errors.New("terminal worker connection failed")
	}
	conn.SetReadLimit(65536)
	p := &rpcPTY{conn: conn, cancel: cancel, done: make(chan struct{})}
	if deadlineErr := conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); deadlineErr != nil {
		closeTransport(p)
		return nil, deadlineErr
	}
	if err = conn.WriteJSON(terminalFrame{Kind: "open", Data: data, Signature: signature(t.key, data)}); err != nil {
		closeTransport(p)
		return nil, errors.New("terminal worker open failed")
	}
	if deadlineErr := conn.SetReadDeadline(time.Now().Add(30 * time.Second)); deadlineErr != nil {
		closeTransport(p)
		return nil, deadlineErr
	}
	var ack terminalFrame
	if err = conn.ReadJSON(&ack); err != nil || ack.Kind != "ready" {
		closeTransport(p)
		return nil, errors.New("terminal worker rejected session")
	}
	if deadlineErr := conn.SetReadDeadline(time.Time{}); deadlineErr != nil {
		closeTransport(p)
		return nil, deadlineErr
	}
	if deadlineErr := conn.SetWriteDeadline(time.Time{}); deadlineErr != nil {
		closeTransport(p)
		return nil, deadlineErr
	}
	go t.renewLease(ctx, envelope.ID)
	go func() { <-ctx.Done(); closeTransport(p) }()
	return p, nil
}
func (p *rpcPTY) Read(b []byte) (int, error) {
	for len(p.remaining) == 0 {
		var frame terminalFrame
		if err := p.conn.ReadJSON(&frame); err != nil {
			closeTransport(p)
			return 0, io.EOF
		}
		switch frame.Kind {
		case "output":
			p.remaining = frame.Data
		case "exit":
			var exitErr error
			if frame.Error != "" {
				exitErr = errors.New("terminal ended without successful exit confirmation")
			}
			p.finish(exitErr)
			return 0, io.EOF
		default:
			closeTransport(p)
			return 0, errors.New("invalid terminal worker frame")
		}
	}
	n := copy(b, p.remaining)
	p.remaining = p.remaining[n:]
	return n, nil
}
func (p *rpcPTY) send(f terminalFrame) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	return p.conn.WriteJSON(f)
}
func (p *rpcPTY) Write(b []byte) (int, error) {
	if len(b) > 4096 {
		return 0, errors.New("terminal input frame too large")
	}
	if err := p.send(terminalFrame{Kind: "input", Data: b}); err != nil {
		return 0, err
	}
	return len(b), nil
}

// Resize changes bounded remote terminal dimensions.
func (p *rpcPTY) Resize(rows, cols int) error {
	if rows < 1 || rows > 500 || cols < 1 || cols > 500 {
		return errors.New("invalid terminal size")
	}
	return p.send(terminalFrame{Kind: "resize", Rows: rows, Cols: cols})
}

// Wait returns success only after an explicit successful remote exit frame.
func (p *rpcPTY) Wait() error { <-p.done; return p.err }

// Close ends a connection with an ambiguous remote outcome.
func (p *rpcPTY) Close() error {
	p.finish(errors.New("terminal connection closed; remote outcome unknown"))
	return nil
}

func (p *rpcPTY) finish(err error) {
	p.once.Do(func() { p.err = err; p.cancel(); closeTransport(p.conn); close(p.done) })
}

// ServePTY accepts a single authenticated, durable-nonce terminal open frame.
// All subsequent input belongs to this connection only: no attach or replay API.
func (h *WorkerHandler) ServePTY(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	// Worker is controller-only. Browser origins must use the authenticated dashboard relay.
	if r.Header.Get("Origin") != "" {
		http.Error(w, "origin denied", http.StatusForbidden)
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		http.Error(w, "worker busy", http.StatusTooManyRequests)
		return
	}
	upgrade := websocket.Upgrader{HandshakeTimeout: 10 * time.Second, CheckOrigin: func(*http.Request) bool { return true }}
	conn, err := upgrade.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer closeTransport(conn)
	conn.SetReadLimit(1 << 20)
	if deadlineErr := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); deadlineErr != nil {
		return
	}
	var first terminalFrame
	if conn.ReadJSON(&first) != nil || first.Kind != "open" || len(first.Data) > 512<<10 || !validSignature(h.key, first.Data, first.Signature) {
		return
	}
	var env terminalEnvelope
	if json.Unmarshal(first.Data, &env) != nil || env.ID == "" || len(env.ID) > 128 || time.Since(env.IssuedAt) > workerLeaseTTL || time.Until(env.IssuedAt) > time.Second || validatePTY(&env.Request) != nil {
		return
	}
	if len(env.Request.Endpoints) < 1 || len(env.Request.Endpoints) > 4 {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), env.Request.Timeout)
	defer cancel()
	for i := range env.Request.Endpoints {
		ep := &env.Request.Endpoints[i]
		ips, resolveErr := net.DefaultResolver.LookupNetIP(ctx, "ip", ep.Address)
		if resolveErr != nil || len(ips) == 0 {
			return
		}
		for _, ip := range ips {
			allowed := false
			for _, prefix := range h.prefixes {
				if prefix.Contains(ip.Unmap()) {
					allowed = true
					break
				}
			}
			if !allowed {
				return
			}
		}
		ep.Address = ips[0].Unmap().String()
	}
	if time.Since(env.IssuedAt) > workerLeaseTTL || h.claim(env.ID) != nil {
		return
	}
	if time.Since(env.IssuedAt) > workerLeaseTTL || ctx.Err() != nil {
		return
	}
	release := h.withLease(ctx, cancel, env.ID, env.IssuedAt)
	defer release()
	terminal, err := (NativeTransport{}).OpenPTY(ctx, env.Request)
	if err != nil {
		return
	}
	defer closeTransport(terminal)
	if deadlineErr := conn.SetReadDeadline(time.Time{}); deadlineErr != nil {
		return
	}
	conn.SetReadLimit(8192)
	if conn.WriteJSON(terminalFrame{Kind: "ready"}) != nil {
		return
	}
	go func() {
		defer cancel()
		window := time.Now()
		frames := 0
		for {
			var frame terminalFrame
			if conn.ReadJSON(&frame) != nil {
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
			switch frame.Kind {
			case "input":
				if _, err := terminal.Write(frame.Data); err != nil {
					return
				}
			case "resize":
				if terminal.Resize(frame.Rows, frame.Cols) != nil {
					return
				}
			default:
				return
			}
		}
	}()
	go func() { <-ctx.Done(); closeTransport(terminal); closeTransport(conn) }()
	buffer := make([]byte, 4096)
	for {
		n, readErr := terminal.Read(buffer)
		if n > 0 {
			if deadlineErr := conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); deadlineErr != nil {
				return
			}
			if conn.WriteJSON(terminalFrame{Kind: "output", Data: buffer[:n]}) != nil {
				return
			}
		}
		if readErr != nil {
			if deadlineErr := conn.SetWriteDeadline(time.Now().Add(time.Second)); deadlineErr != nil {
				return
			}
			exitFrame := terminalFrame{Kind: "exit"}
			if terminal.Wait() != nil {
				exitFrame.Error = "unknown_or_failed_exit"
			}
			if err := conn.WriteJSON(exitFrame); err != nil {
				return
			}
			return
		}
	}
}

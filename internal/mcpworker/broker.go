// Package mcpworker runs approved MCP images in rootless Podman containers. The
// control plane never executes arbitrary local commands or shares its vault.
package mcpworker

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

var errDenied = errors.New("MCP worker denied")

// Profile pins a deployment-approved image digest, fixed arguments and network.
type Profile struct {
	Image   string   `json:"image"`
	Args    []string `json:"args"`
	Network string   `json:"network"`
}

// Lease authorizes one short-lived owner and connection worker session.
type Lease struct {
	Owner      string `json:"owner"`
	Connection string `json:"connection"`
	Generation int64  `json:"generation"`
	Profile    string `json:"profile"`
	Nonce      string `json:"nonce"`
	Expires    int64  `json:"expires"`
}

// Bootstrap carries only the credential explicitly authorized for the isolated process.
type Bootstrap struct {
	Token string `json:"token,omitempty"`
}

// Broker authenticates leases and bridges bounded protocol frames to isolated processes.
type Broker struct {
	Key      []byte
	Profiles map[string]Profile
	StateDir string
	Slots    chan struct{}
	command  func(context.Context, []string) *exec.Cmd
	mu       sync.Mutex
	closing  bool
	sessions map[string]context.CancelFunc
	workers  sync.WaitGroup
}

var imagePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9./:_-]*@sha256:[a-f0-9]{64}$`)
var namePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)

func validateProfile(p Profile) error {
	if !imagePattern.MatchString(p.Image) || len(p.Args) > 32 {
		return errDenied
	}
	if p.Network != "" && p.Network != "none" && (!strings.HasPrefix(p.Network, "iulita-mcp-") || !namePattern.MatchString(p.Network)) {
		return errDenied
	}
	for _, a := range p.Args {
		if len(a) > 4096 {
			return errDenied
		}
	}
	return nil
}

// NewBroker validates deployment profiles and the private replay-protection directory.
func NewBroker(key []byte, profiles map[string]Profile, state string) (*Broker, error) {
	if len(key) < 32 || state == "" {
		return nil, errDenied
	}
	for name, p := range profiles {
		if !namePattern.MatchString(name) || validateProfile(p) != nil {
			return nil, errDenied
		}
	}
	if e := os.MkdirAll(state, 0o700); e != nil {
		return nil, e
	}
	info, e := os.Stat(state)
	if e != nil || info.Mode().Perm()&0o077 != 0 {
		return nil, errDenied
	}
	return &Broker{Key: append([]byte(nil), key...), Profiles: profiles, StateDir: state, Slots: make(chan struct{}, 4), sessions: map[string]context.CancelFunc{}}, nil
}

// Shutdown closes hijacked WebSockets and waits for container cleanup. HTTP
// Server.Shutdown alone does not wait for upgraded connections.
func (b *Broker) Shutdown(ctx context.Context) error {
	b.mu.Lock()
	b.closing = true
	for _, cancel := range b.sessions {
		cancel()
	}
	b.mu.Unlock()
	done := make(chan struct{})
	go func() { b.workers.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func signature(key []byte, payload string) string {
	m := hmac.New(sha256.New, key)
	_, _ = m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
func (b *Broker) verify(r *http.Request) (Lease, Profile, error) {
	raw := r.Header.Get("X-Iulita-MCP-Lease")
	sig := r.Header.Get("X-Iulita-MCP-Signature")
	if len(raw) > 4096 || !hmac.Equal([]byte(signature(b.Key, raw)), []byte(sig)) {
		return Lease{}, Profile{}, errDenied
	}
	data, e := base64.RawURLEncoding.DecodeString(raw)
	if e != nil {
		return Lease{}, Profile{}, errDenied
	}
	var l Lease
	if json.Unmarshal(data, &l) != nil || l.Owner == "" || l.Connection == "" || l.Generation < 1 || l.Expires < time.Now().Unix() || l.Expires > time.Now().Add(6*time.Minute).Unix() || !namePattern.MatchString(l.Nonce) {
		return Lease{}, Profile{}, errDenied
	}
	p, ok := b.Profiles[l.Profile]
	if !ok {
		return Lease{}, Profile{}, errDenied
	}
	// Nonces survive broker restarts, but never grow without a retention cap.
	dir, err := os.Open(b.StateDir)
	if err != nil {
		return Lease{}, Profile{}, errDenied
	}
	entries, readErr := dir.ReadDir(10001)
	closeQuietly(dir)
	if readErr != nil && readErr != io.EOF {
		return Lease{}, Profile{}, errDenied
	}
	if len(entries) > 10000 {
		return Lease{}, Profile{}, errDenied
	}
	for _, entry := range entries {
		if entry.Type().IsRegular() {
			if info, err := entry.Info(); err == nil && time.Since(info.ModTime()) > 10*time.Minute {
				if removeErr := os.Remove(filepath.Join(b.StateDir, entry.Name())); removeErr != nil {
					slog.Warn("MCP nonce cleanup incomplete")
				}
			}
		}
	}
	f, e := os.OpenFile(filepath.Join(b.StateDir, l.Nonce), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if e != nil {
		return Lease{}, Profile{}, errDenied
	}
	closeQuietly(f)
	return l, p, nil
}

// PodmanArgs builds fixed resource and isolation options before the pinned image.
func PodmanArgs(p Profile, name string, withToken bool) []string {
	network := p.Network
	if network == "" {
		network = "none"
	}
	args := []string{"run", "--rm", "--timeout", "360", "--interactive", "--pull=never", "--name", name, "--read-only", "--log-driver", "none", "--image-volume", "ignore", "--network", network, "--user", "65532:65532", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "64", "--memory", "256m", "--cpus", "1", "--tmpfs", "/tmp:rw,noexec,nosuid,size=16m"}
	if withToken {
		args = append(args, "--env", "MCP_ACCESS_TOKEN")
	}
	args = append(args, p.Image)
	return append(args, p.Args...)
}
func minimalEnv() []string {
	out := []string{}
	for _, k := range []string{"PATH", "HOME", "XDG_RUNTIME_DIR"} {
		if v := os.Getenv(k); v != "" {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// ServeHTTP rejects browser origins and unauthenticated or replayed process leases.
func (b *Broker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/stdio" || r.Method != "GET" || r.Header.Get("Origin") != "" {
		http.Error(w, "denied", http.StatusForbidden)
		return
	}
	lease, profile, e := b.verify(r)
	if e != nil {
		http.Error(w, "denied", http.StatusForbidden)
		return
	}
	select {
	case b.Slots <- struct{}{}:
		defer func() { <-b.Slots }()
	default:
		http.Error(w, "busy", http.StatusTooManyRequests)
		return
	}
	deadline := time.Unix(lease.Expires, 0)
	ctx, cancel := context.WithDeadline(r.Context(), deadline)
	id := uuid.NewString()
	b.mu.Lock()
	if b.closing {
		b.mu.Unlock()
		cancel()
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	b.sessions[id] = cancel
	b.workers.Add(1)
	b.mu.Unlock()
	defer func() {
		cancel()
		b.mu.Lock()
		delete(b.sessions, id)
		b.mu.Unlock()
		b.workers.Done()
	}()
	ws, e := (&websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" }}).Upgrade(w, r, nil)
	if e != nil {
		return
	}
	defer closeQuietly(ws)
	stopClose := context.AfterFunc(ctx, func() { closeQuietly(ws) })
	defer stopClose()
	ws.SetReadLimit(8 << 20)
	if err := ws.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return
	}
	_, data, e := ws.ReadMessage()
	if e != nil || len(data) > 32768 {
		return
	}
	var bootstrap Bootstrap
	if json.Unmarshal(data, &bootstrap) != nil || strings.ContainsAny(bootstrap.Token, "\r\n") {
		return
	}
	if err := ws.SetReadDeadline(deadline); err != nil {
		return
	}
	if err := ws.SetWriteDeadline(deadline); err != nil {
		return
	}
	name := "iulita-mcp-" + uuid.NewString()
	//nolint:gosec // G204: fixed rootless launcher; digest, network and fixed args are deployment-validated.
	cmd := exec.CommandContext(ctx, "podman", PodmanArgs(profile, name, bootstrap.Token != "")...)
	if b.command != nil {
		cmd = b.command(ctx, PodmanArgs(profile, name, bootstrap.Token != ""))
	}
	cmd.Env = minimalEnv()
	if bootstrap.Token != "" {
		cmd.Env = append(cmd.Env, "MCP_ACCESS_TOKEN="+bootstrap.Token) // gitleaks:allow runtime value from the owner-scoped bootstrap, no literal credential
	}
	cmd.Stderr = io.Discard
	input, e := cmd.StdinPipe()
	if e != nil {
		return
	}
	output, e := cmd.StdoutPipe()
	if e != nil {
		return
	}
	if cmd.Start() != nil {
		return
	}
	defer func() {
		cancel()
		closeQuietly(input)
		closeQuietly(ws)
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		//nolint:gosec // G204: fixed cleanup command; container name is generated locally as a UUID.
		rm := exec.CommandContext(cleanup, "podman", "rm", "--force", name)
		rm.Env = minimalEnv()
		if removeErr := rm.Run(); removeErr != nil {
			slog.Warn("MCP container cleanup requires attention")
		}
		if waitErr := cmd.Wait(); waitErr != nil {
			slog.Debug("MCP worker process exited without a clean status")
		}
	}()
	done := make(chan struct{}, 2)
	go func() {
		defer func() { done <- struct{}{} }()
		total := 0
		for {
			_, message, er := ws.ReadMessage()
			if er != nil {
				return
			}
			total += len(message)
			if total > 32<<20 || !json.Valid(message) {
				return
			}
			if _, er = input.Write(append(message, '\n')); er != nil {
				return
			}
		}
	}()
	go func() {
		defer func() { done <- struct{}{} }()
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 4096), 8<<20)
		total := 0
		for scanner.Scan() {
			message := scanner.Bytes()
			total += len(message)
			if total > 32<<20 || !json.Valid(message) {
				return
			}
			if ws.WriteMessage(websocket.TextMessage, message) != nil {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// Client creates one signed, bounded broker lease for one operation. Cancellation
// closes the stream, causing the broker to remove the entire container.
type Client struct {
	URL    string
	Key    []byte
	Dialer *websocket.Dialer
}

// NewClient validates the worker endpoint and private protocol key.
func NewClient(endpoint string, key []byte) (*Client, error) {
	u, e := url.Parse(endpoint)
	if e != nil || len(key) < 32 || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errDenied
	}
	d := &websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	if u.Scheme == "unix" {
		socket := u.Path
		if !filepath.IsAbs(socket) {
			return nil, errDenied
		}
		d.NetDialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}
		endpoint = "ws://localhost/stdio"
	} else {
		if u.Scheme != "wss" {
			return nil, errDenied
		}
		u.Path = "/stdio"
		endpoint = u.String()
	}
	return &Client{URL: endpoint, Key: append([]byte(nil), key...), Dialer: d}, nil
}

// Transport opens a signed, cancellation-bound lease for one owner runtime.
func (c *Client) Transport(ctx context.Context, owner, connection string, generation int64, profile, token string) (sdk.Transport, error) {
	lease := Lease{Owner: owner, Connection: connection, Generation: generation, Profile: profile, Nonce: uuid.NewString(), Expires: time.Now().Add(5 * time.Minute).Unix()}
	if d, ok := ctx.Deadline(); ok && d.Unix() < lease.Expires {
		lease.Expires = d.Unix()
	}
	data, marshalErr := json.Marshal(lease)
	if marshalErr != nil {
		return nil, errDenied
	}
	raw := base64.RawURLEncoding.EncodeToString(data)
	h := http.Header{"X-Iulita-Mcp-Lease": {raw}, "X-Iulita-Mcp-Signature": {signature(c.Key, raw)}}
	ws, resp, e := c.Dialer.DialContext(ctx, c.URL, h)
	if resp != nil && resp.Body != nil {
		defer func() {
			if bodyErr := resp.Body.Close(); bodyErr != nil {
				slog.Debug("MCP handshake cleanup incomplete")
			}
		}()
	}
	if e != nil {

		return nil, errDenied
	}
	ws.SetReadLimit(8 << 20)
	stopClose := context.AfterFunc(ctx, func() { closeQuietly(ws) })
	deadline := time.Now().Add(10 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := ws.SetWriteDeadline(deadline); err != nil {
		stopClose()
		closeQuietly(ws)
		return nil, errDenied
	}
	if e := ws.WriteJSON(Bootstrap{Token: token}); e != nil {
		stopClose()
		closeQuietly(ws)
		return nil, errDenied
	}
	conn := &wsConnection{ws: ws, stopClose: stopClose}
	return &wsTransport{conn}, nil
}

type wsTransport struct{ c *wsConnection }

// Connect returns a single bounded logical JSON-RPC connection.
func (t *wsTransport) Connect(context.Context) (sdk.Connection, error) { return t.c, nil }

type wsConnection struct {
	ws        *websocket.Conn
	mu        sync.Mutex
	once      sync.Once
	closeErr  error
	stopClose func() bool
}

// Read decodes one size-limited JSON-RPC frame.
func (c *wsConnection) Read(context.Context) (jsonrpc.Message, error) {
	_, b, e := c.ws.ReadMessage()
	if e != nil {
		return nil, errDenied
	}
	return jsonrpc.DecodeMessage(b)
}

// Write serializes one bounded JSON-RPC frame under a write deadline.
func (c *wsConnection) Write(ctx context.Context, m jsonrpc.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, e := jsonrpc.EncodeMessage(m)
	if e != nil || len(b) > 8<<20 {
		return errDenied
	}
	if d, ok := ctx.Deadline(); ok {
		if err := c.ws.SetWriteDeadline(d); err != nil {
			return errDenied
		}
	} else {
		if err := c.ws.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
			return errDenied
		}
	}
	return c.ws.WriteMessage(websocket.TextMessage, b)
}

// Close shuts down the broker stream once and unblocks pending reads.
func (c *wsConnection) Close() error {
	c.once.Do(func() {
		if c.stopClose != nil {
			c.stopClose()
		}
		c.closeErr = c.ws.Close()
	})
	return c.closeErr
}

// SessionID reports that broker streams have no independent protocol session identifier.
func (c *wsConnection) SessionID() string { return "" }

// Cleanup failures are logged without process output, tokens or request data.
func closeQuietly(c io.Closer) {
	if err := c.Close(); err != nil {
		slog.Debug("MCP worker stream cleanup incomplete")
	}
}

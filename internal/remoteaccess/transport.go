package remoteaccess

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// TransportEndpoint contains short-lived resolved credentials. Never log this value.
type TransportEndpoint struct {
	Address                          string
	Port                             int
	Username                         string
	HostKeys                         []string
	Password, PrivateKey, Passphrase string
}

// TransportRequest fixes the route, remote command and total time budget.
type TransportRequest struct {
	Operation  string
	File       *FileRequest
	Background *BackgroundRequest
	Endpoints  []TransportEndpoint
	Command    string
	Timeout    time.Duration
}

// TransportResult separates remote exit status from ambiguous transport loss.
type TransportResult struct {
	File           *FileResult
	Background     *BackgroundResult
	ExitCode       *int
	Stdout, Stderr []byte
	Truncated      bool
	Dispatched     bool
	ErrorCode      string
}

// NativeTransport performs no pooling, agent forwarding, PTY, or automatic retries.
type NativeTransport struct{}

const streamLimit = 1 << 20
const totalOutputLimit = 10 << 20

func endpointConfig(e TransportEndpoint) (*ssh.ClientConfig, error) {
	if e.Address == "" || strings.ContainsAny(e.Address, "\x00\r\n") || e.Port < 1 || e.Port > 65535 || e.Username == "" || len(e.HostKeys) == 0 {
		return nil, errors.New("invalid SSH endpoint or missing host trust")
	}
	keys := make([]ssh.PublicKey, 0, len(e.HostKeys))
	for _, raw := range e.HostKeys {
		key, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(raw))
		if err != nil || len(bytes.TrimSpace(rest)) != 0 {
			return nil, errors.New("invalid pinned SSH host key")
		}
		if _, cert := key.(*ssh.Certificate); cert {
			return nil, errors.New("SSH host certificates require a separate trust policy")
		}
		keys = append(keys, key)
	}
	var auth []ssh.AuthMethod
	if e.PrivateKey != "" {
		var signer ssh.Signer
		var err error
		if e.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(e.PrivateKey), []byte(e.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(e.PrivateKey))
		}
		if err != nil {
			return nil, errors.New("invalid SSH private key or passphrase")
		}
		auth = append(auth, ssh.PublicKeys(signer))
	}
	if e.Password != "" {
		auth = append(auth, ssh.Password(e.Password))
	}
	if len(auth) != 1 {
		return nil, errors.New("exactly one SSH authentication method is required")
	}
	return &ssh.ClientConfig{User: e.Username, Auth: auth, HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
		for _, pinned := range keys {
			if bytes.Equal(pinned.Marshal(), key.Marshal()) {
				return nil
			}
		}
		return errors.New("SSH host key does not match pinned trust")
	}}, nil
}

// Run connects to each verified hop and executes the command once.
func (NativeTransport) Run(parent context.Context, req TransportRequest) (result TransportResult, err error) {
	fail := func(code string) (TransportResult, error) {
		result.ErrorCode = code
		return result, fmt.Errorf("SSH operation failed: %s", code)
	}
	if len(req.Endpoints) < 1 || len(req.Endpoints) > 4 || ((req.Operation == "" || req.Operation == "exec") && (req.Command == "" || len(req.Command) > 64<<10)) {
		return fail("invalid_request")
	}
	if req.Timeout > 15*time.Minute {
		return fail("invalid_timeout")
	}
	client, connection, e := ConnectNative(parent, req.Endpoints, req.Timeout)
	if e != nil {
		return fail("connect_failed")
	}
	defer closeTransport(connection)
	ctx := parent
	if req.Operation != "" && req.Operation != "exec" {
		return runNativeOperation(ctx, client, req)
	}
	session, e := client.NewSession()
	if e != nil {
		return fail("session_failed")
	}
	defer func() {
		closeTransport(connection)
		closeTransport(session)
	}()
	capture := &outputCapture{close: func() { closeTransport(connection) }}
	session.Stdout = &outputStream{capture: capture}
	session.Stderr = &outputStream{capture: capture, stderr: true}
	if ctx.Err() != nil {
		return fail("cancelled")
	}
	// Once the request is sent, loss of its acknowledgement is an ambiguous outcome.
	result.Dispatched = true
	if e = session.Start(req.Command); e == nil {
		e = session.Wait()
	}
	capture.mu.Lock()
	result.Stdout = append([]byte(nil), capture.stdout...)
	result.Stderr = append([]byte(nil), capture.stderr...)
	result.Truncated = capture.truncated
	hardLimit := capture.total > totalOutputLimit
	capture.mu.Unlock()
	if e == nil {
		code := 0
		result.ExitCode = &code
		return result, nil
	}
	var exitErr *ssh.ExitError
	if errors.As(e, &exitErr) && exitErr.ExitStatus() >= 0 {
		code := exitErr.ExitStatus()
		result.ExitCode = &code
		return result, nil
	}
	if hardLimit {
		return fail("output_limit_unknown_outcome")
	}
	return fail("unknown_outcome")
}

type outputCapture struct {
	mu             sync.Mutex
	stdout, stderr []byte
	total          int64
	truncated      bool
	close          func()
}
type outputStream struct {
	capture *outputCapture
	stderr  bool
}

func (w *outputStream) Write(p []byte) (int, error) {
	c := w.capture
	c.mu.Lock()
	defer c.mu.Unlock()
	c.total += int64(len(p))
	dst := &c.stdout
	if w.stderr {
		dst = &c.stderr
	}
	remaining := streamLimit - len(*dst)
	n := len(p)
	if n > remaining {
		n = remaining
		c.truncated = true
	}
	*dst = append(*dst, p[:n]...)
	if c.total > totalOutputLimit {
		c.truncated = true
		c.close()
	}
	return len(p), nil
}

// closeTransport is best-effort teardown after outcome capture or cancellation.
// A close error cannot establish a remote command outcome and must not replace
// the already captured exit status/unknown outcome or interrupt remaining cleanup.
func closeTransport(connection io.Closer) {
	if err := connection.Close(); err != nil {
		return
	}
}

// ConnectNative establishes pinned, authenticated multi-hop SSH connections.
func ConnectNative(parent context.Context, endpoints []TransportEndpoint, timeout time.Duration) (*ssh.Client, io.Closer, error) {
	if len(endpoints) < 1 || len(endpoints) > 4 {
		return nil, nil, errors.New("invalid route")
	}
	configs := make([]*ssh.ClientConfig, len(endpoints))
	seen := map[string]bool{}
	for i, e := range endpoints {
		config, configErr := endpointConfig(e)
		if configErr != nil {
			return nil, nil, fmt.Errorf("invalid_configuration")
		}
		configs[i] = config
		address := net.JoinHostPort(e.Address, strconv.Itoa(e.Port))
		if seen[address] {
			return nil, nil, fmt.Errorf("route_cycle")
		}
		seen[address] = true
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	if timeout > time.Hour {
		return nil, nil, fmt.Errorf("invalid_timeout")
	}
	ctx, cancel := context.WithTimeout(parent, timeout)

	success := false
	defer func() {
		if !success {
			cancel()
		}
	}()
	root, e := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(endpoints[0].Address, strconv.Itoa(endpoints[0].Port)))
	if e != nil {
		return nil, nil, fmt.Errorf("connect_failed")
	}

	defer func() {
		if !success {
			closeTransport(root)
		}
	}()
	watchDone := make(chan struct{})

	go func() {
		select {
		case <-ctx.Done():
			closeTransport(root)
		case <-watchDone:
		}
	}()
	connectDeadline := time.Now().Add(30 * time.Second)
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(connectDeadline) {
		connectDeadline = deadline
	}
	if e = root.SetDeadline(connectDeadline); e != nil {
		return nil, nil, fmt.Errorf("connect_failed")
	}
	var clients []*ssh.Client
	connection := root
	for i, ep := range endpoints {
		hopDeadline := time.Now().Add(10 * time.Second)
		if connectDeadline.Before(hopDeadline) {
			hopDeadline = connectDeadline
		}
		if e = root.SetDeadline(hopDeadline); e != nil {
			return nil, nil, fmt.Errorf("connect_failed")
		}
		address := net.JoinHostPort(ep.Address, strconv.Itoa(ep.Port))
		if i > 0 {
			connection, e = clients[i-1].Dial("tcp", address)
			if e != nil {
				return nil, nil, fmt.Errorf("jump_failed")
			}
		}
		clientConn, channels, requests, handshakeErr := ssh.NewClientConn(connection, address, configs[i])
		if handshakeErr != nil {
			closeTransport(root)
			closeTransport(connection)
			return nil, nil, fmt.Errorf("handshake_failed")
		}
		clients = append(clients, ssh.NewClient(clientConn, channels, requests))
	}
	if e = root.SetDeadline(time.Time{}); e != nil {
		return nil, nil, fmt.Errorf("connect_failed")
	}
	if ctx.Err() != nil {
		return nil, nil, fmt.Errorf("cancelled")
	}
	success = true
	return clients[len(clients)-1], &nativeConnection{root: root, clients: clients, cancel: cancel, done: watchDone}, nil
}

type nativeConnection struct {
	root    net.Conn
	clients []*ssh.Client
	cancel  context.CancelFunc
	done    chan struct{}
	once    sync.Once
}

// Close tears down the root socket before nested SSH clients.
func (n *nativeConnection) Close() error {
	n.once.Do(func() {
		n.cancel()
		close(n.done)
		closeTransport(n.root)
		for i := len(n.clients) - 1; i >= 0; i-- {
			closeTransport(n.clients[i])
		}
	})
	return nil
}

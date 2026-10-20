package remoteaccess

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// PTYRequest opens an explicitly human-operated terminal. Authentication is only
// performed by SSH; credentials must never be injected through terminal input.
type PTYRequest struct {
	Endpoints  []TransportEndpoint
	Rows, Cols int
	Term       string
	Timeout    time.Duration
}

// PTYSession is a single-owner human terminal without reattachment.
type PTYSession interface {
	io.ReadWriteCloser
	Resize(rows, cols int) error
	Wait() error
}

// PTYTransport opens authenticated human terminal sessions.
type PTYTransport interface {
	OpenPTY(context.Context, PTYRequest) (PTYSession, error)
}

func validatePTY(req *PTYRequest) error {
	if req.Rows < 1 || req.Rows > 500 || req.Cols < 1 || req.Cols > 500 {
		return errors.New("invalid terminal size")
	}
	if req.Term == "" {
		req.Term = "xterm-256color"
	}
	if req.Term != "xterm" && req.Term != "xterm-256color" && req.Term != "vt100" {
		return errors.New("unsupported terminal type")
	}
	if req.Timeout == 0 {
		req.Timeout = time.Hour
	}
	if req.Timeout < time.Second || req.Timeout > time.Hour {
		return errors.New("invalid terminal lifetime")
	}
	return nil
}

type nativePTY struct {
	outputBytes  int64
	session      *ssh.Session
	connection   io.Closer
	input        io.WriteCloser
	output       *io.PipeReader
	outputWriter *io.PipeWriter
	cancel       context.CancelFunc
	once         sync.Once
	done         chan struct{}
	err          error
	mu           sync.Mutex
	lastInput    time.Time
	rateStart    time.Time
	rateBytes    int
}

// OpenPTY opens one shell without forwarding or replay.
func (NativeTransport) OpenPTY(parent context.Context, req PTYRequest) (PTYSession, error) {
	if err := validatePTY(&req); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, req.Timeout)
	client, connection, err := ConnectNative(ctx, req.Endpoints, req.Timeout)
	if err != nil {
		cancel()
		return nil, err
	}
	session, err := client.NewSession()
	if err != nil {
		cancel()
		closeTransport(connection)
		return nil, err
	}
	input, err := session.StdinPipe()
	if err != nil {
		cancel()
		closeTransport(connection)
		closeTransport(session)
		return nil, err
	}
	reader, writer := io.Pipe()
	session.Stdout = writer
	session.Stderr = writer
	p := &nativePTY{session: session, connection: connection, input: input, output: reader, outputWriter: writer, cancel: cancel, done: make(chan struct{}), lastInput: time.Now()}
	if err = session.RequestPty(req.Term, req.Rows, req.Cols, ssh.TerminalModes{ssh.ECHO: 1}); err == nil {
		err = session.Shell()
	}
	if err != nil {
		closeTransport(p)
		closeTransport(writer)
		return nil, errors.New("terminal start failed; remote outcome unknown")
	}
	go func() { p.err = session.Wait(); closeTransport(writer); close(p.done); closeTransport(p) }()
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				closeTransport(p)
				return
			case <-p.done:
				return
			case <-ticker.C:
				p.mu.Lock()
				idle := time.Since(p.lastInput) > 15*time.Minute
				p.mu.Unlock()
				if idle {
					closeTransport(p)
					return
				}
			}
		}
	}()
	return p, nil
}
func (p *nativePTY) Read(b []byte) (int, error) {
	n, err := p.output.Read(b)
	p.mu.Lock()
	p.outputBytes += int64(n)
	exceeded := p.outputBytes > totalOutputLimit
	p.mu.Unlock()
	if exceeded {
		closeTransport(p)
		return 0, errors.New("terminal output quota exceeded; remote outcome unknown")
	}
	return n, err
}
func (p *nativePTY) Write(b []byte) (int, error) {
	p.mu.Lock()
	if len(b) > 4096 {
		p.mu.Unlock()
		return 0, errors.New("terminal input frame too large")
	}
	now := time.Now()
	if now.Sub(p.rateStart) >= time.Second {
		p.rateStart = now
		p.rateBytes = 0
	}
	if p.rateBytes+len(b) > 65536 {
		p.mu.Unlock()
		return 0, errors.New("terminal input rate exceeded")
	}
	p.rateBytes += len(b)
	p.lastInput = now
	p.mu.Unlock()
	return p.input.Write(b)
}

// Resize changes bounded terminal dimensions.
func (p *nativePTY) Resize(rows, cols int) error {
	if rows < 1 || rows > 500 || cols < 1 || cols > 500 {
		return errors.New("invalid terminal size")
	}
	return p.session.WindowChange(rows, cols)
}

// Wait reports the confirmed exit or ambiguous disconnect.
func (p *nativePTY) Wait() error { <-p.done; return p.err }

// Close disconnects transport without promising remote process termination.
func (p *nativePTY) Close() error {
	p.once.Do(func() {
		p.cancel()
		_ = p.outputWriter.CloseWithError(context.Canceled)
		closeTransport(p.connection)
		closeTransport(p.session)
	})
	return nil
}

package remoteaccess

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"golang.org/x/crypto/ssh"
)

func terminalTestServer(t *testing.T) TransportEndpoint {
	t.Helper()
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(key)
	cfg := &ssh.ServerConfig{PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) { return nil, nil }}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			raw, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer raw.Close()
				conn, chs, rs, err := ssh.NewServerConn(raw, cfg)
				if err != nil {
					return
				}
				defer conn.Close()
				go ssh.DiscardRequests(rs)
				for newch := range chs {
					ch, reqs, err := newch.Accept()
					if err != nil {
						continue
					}
					go func() {
						defer ch.Close()
						for req := range reqs {
							switch req.Type {
							case "pty-req", "window-change":
								_ = req.Reply(true, nil)
							case "shell":
								_ = req.Reply(true, nil)
								go func() { _, _ = io.Copy(ch, ch) }()
							default:
								_ = req.Reply(false, nil)
							}
						}
					}()
				}
			}()
		}
	}()
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	n, _ := strconv.Atoi(port)
	return TransportEndpoint{Address: host, Port: n, Username: "test", Password: "test", HostKeys: []string{string(ssh.MarshalAuthorizedKey(signer.PublicKey()))}}
}
func TestNativePTYInteractiveAndCancel(t *testing.T) {
	ep := terminalTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session, err := (NativeTransport{}).OpenPTY(ctx, PTYRequest{Endpoints: []TransportEndpoint{ep}, Rows: 24, Cols: 80})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err = session.Resize(40, 100); err != nil {
		t.Fatal(err)
	}
	if _, err = session.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 5)
	if _, err = io.ReadFull(session, b); err != nil || string(b) != "hello" {
		t.Fatalf("echo %q %v", b, err)
	}
	if _, err = session.Write(make([]byte, 4097)); err == nil {
		t.Fatal("oversized input accepted")
	}
	if session.Resize(501, 80) == nil {
		t.Fatal("oversized terminal accepted")
	}
	cancel()
	done := make(chan struct{})
	go func() { _ = session.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not stop PTY")
	}
}
func TestPTYValidation(t *testing.T) {
	for _, req := range []PTYRequest{{Rows: 0, Cols: 80}, {Rows: 24, Cols: 501}, {Rows: 24, Cols: 80, Term: "xterm;exec"}, {Rows: 24, Cols: 80, Timeout: 2 * time.Hour}} {
		if validatePTY(&req) == nil {
			t.Fatal("invalid terminal accepted")
		}
	}
}

func TestPTYWorkerRPC(t *testing.T) {
	ep := terminalTestServer(t)
	key := []byte("01234567890123456789012345678901")
	state := t.TempDir()
	if err := os.Chmod(state, 0o700); err != nil {
		t.Fatal(err)
	}
	h, err := NewWorkerHandler(key, []string{"127.0.0.0/8"}, state)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/renew" {
			h.renew(w, r)
		} else {
			h.ServePTY(w, r)
		}
	}))
	defer server.Close()
	transport, err := NewRPCTransport(server.URL, key)
	if err != nil {
		t.Fatal(err)
	}
	transport.client = server.Client()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session, err := transport.OpenPTY(ctx, PTYRequest{Endpoints: []TransportEndpoint{ep}, Rows: 24, Cols: 80})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err = session.Write([]byte("rpc")); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 3)
	if _, err = io.ReadFull(session, b); err != nil || string(b) != "rpc" {
		t.Fatalf("echo %q %v", b, err)
	}
	if session.Resize(30, 100) != nil {
		t.Fatal("resize failed")
	}
	cancel()
	done := make(chan struct{})
	go func() {
		if session.Wait() == nil {
			t.Error("cancel reported terminal success")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RPC cancellation blocked")
	}
}

type discardPTYInput struct{}

func (discardPTYInput) Write(b []byte) (int, error) { return len(b), nil }
func (discardPTYInput) Close() error                { return nil }
func TestPTYInputRateLimit(t *testing.T) {
	p := &nativePTY{input: discardPTYInput{}}
	for i := 0; i < 16; i++ {
		if _, err := p.Write(make([]byte, 4096)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.Write([]byte("x")); err == nil {
		t.Fatal("rate limit bypassed")
	}
}

func TestPTYWorkerRejectsAuthenticationAndReplay(t *testing.T) {
	ep := terminalTestServer(t)
	key := []byte("01234567890123456789012345678901")
	state := t.TempDir()
	_ = os.Chmod(state, 0o700)
	h, err := NewWorkerHandler(key, []string{"127.0.0.0/8"}, state)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(h.ServePTY))
	defer server.Close()
	transport, _ := NewRPCTransport(server.URL, key)
	transport.client = server.Client()
	rt := server.Client().Transport.(*http.Transport)
	dialer := websocket.Dialer{TLSClientConfig: rt.TLSClientConfig}
	endpoint := "wss" + strings.TrimPrefix(server.URL, "https") + "/terminal"
	env := terminalEnvelope{ID: "once-only", IssuedAt: time.Now().UTC(), Request: PTYRequest{Endpoints: []TransportEndpoint{ep}, Rows: 24, Cols: 80}}
	data, _ := json.Marshal(env)
	attempt := func(sig string, wantReady bool) {
		t.Helper()
		conn, _, err := dialer.Dial(endpoint, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		if err = conn.WriteJSON(terminalFrame{Kind: "open", Data: data, Signature: sig}); err != nil {
			t.Fatal(err)
		}
		var frame terminalFrame
		err = conn.ReadJSON(&frame)
		if wantReady && (err != nil || frame.Kind != "ready") {
			t.Fatalf("expected ready: %v %s", err, frame.Kind)
		}
		if !wantReady && err == nil {
			t.Fatal("unauthorized/replayed terminal accepted")
		}
	}
	attempt("wrong", false)
	attempt(signature(key, data), true)
	attempt(signature(key, data), false)
	headers := http.Header{}
	headers.Set("Origin", "https://untrusted.example")
	if conn, _, err := dialer.Dial(endpoint, headers); err == nil {
		conn.Close()
		t.Fatal("browser origin accepted")
	}
}

func TestPTYAbsoluteAndIdleLimits(t *testing.T) {
	for _, idle := range []bool{false, true} {
		t.Run(map[bool]string{false: "absolute", true: "idle"}[idle], func(t *testing.T) {
			ep := terminalTestServer(t)
			timeout := time.Second
			if idle {
				timeout = time.Minute
			}
			session, err := (NativeTransport{}).OpenPTY(context.Background(), PTYRequest{Endpoints: []TransportEndpoint{ep}, Rows: 24, Cols: 80, Timeout: timeout})
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			if idle {
				p := session.(*nativePTY)
				p.mu.Lock()
				p.lastInput = time.Now().Add(-16 * time.Minute)
				p.mu.Unlock()
			}
			done := make(chan error, 1)
			go func() { done <- session.Wait() }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("expired terminal reported success")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("expired terminal remained open")
			}
		})
	}
}

func TestPTYOutputQuotaClosesAsUnknown(t *testing.T) {
	endpoint := terminalTestServer(t)
	session, err := (NativeTransport{}).OpenPTY(context.Background(), PTYRequest{Endpoints: []TransportEndpoint{endpoint}, Rows: 24, Cols: 80, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	native, ok := session.(*nativePTY)
	if !ok {
		t.Fatal("native PTY expected")
	}
	native.mu.Lock()
	native.outputBytes = totalOutputLimit
	native.mu.Unlock()
	if _, err = session.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err = session.Read(make([]byte, 1)); err == nil {
		t.Fatal("output cap ignored")
	}
	if err = session.Wait(); err == nil {
		t.Fatal("quota disconnect claimed remote success")
	}
}

package remoteaccess

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"io"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func testSSHServer(t *testing.T, authorized ...ssh.PublicKey) (TransportEndpoint, *atomic.Int32) {
	t.Helper()
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(private)
	auths := new(atomic.Int32)
	config := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		auths.Add(1)
		if c.User() == "test" && string(password) == "password" {
			return nil, nil
		}
		return nil, io.EOF
	}, PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		auths.Add(1)
		if len(authorized) > 0 && bytes.Equal(key.Marshal(), authorized[0].Marshal()) {
			return nil, nil
		}
		return nil, io.EOF
	}}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	connections := make(chan net.Conn, 128)
	done := make(chan struct{})
	t.Cleanup(func() {
		close(done)
		listener.Close()
		for {
			select {
			case c := <-connections:
				c.Close()
			default:
				return
			}
		}
	})
	go func() {
		for {
			raw, e := listener.Accept()
			if e != nil {
				return
			}
			select {
			case connections <- raw:
			case <-done:
				raw.Close()
				return
			}
			go func() {
				conn, chans, requests, e := ssh.NewServerConn(raw, config)
				if e != nil {
					raw.Close()
					return
				}
				defer conn.Close()
				go ssh.DiscardRequests(requests)
				for ch := range chans {
					switch ch.ChannelType() {
					case "direct-tcpip":
						var p struct {
							Host       string
							Port       uint32
							Origin     string
							OriginPort uint32
						}
						if ssh.Unmarshal(ch.ExtraData(), &p) != nil {
							ch.Reject(ssh.ConnectionFailed, "bad payload")
							continue
						}
						up, e := net.Dial("tcp", net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))))
						if e != nil {
							ch.Reject(ssh.ConnectionFailed, "unreachable")
							continue
						}
						channel, reqs, e := ch.Accept()
						if e != nil {
							up.Close()
							continue
						}
						go ssh.DiscardRequests(reqs)
						go func() {
							defer channel.Close()
							defer up.Close()
							go func() { io.Copy(up, channel); up.Close() }()
							io.Copy(channel, up)
						}()
					case "session":
						channel, reqs, e := ch.Accept()
						if e != nil {
							continue
						}
						go func() {
							defer channel.Close()
							for req := range reqs {
								if req.Type != "exec" {
									req.Reply(false, nil)
									continue
								}
								var p struct{ Command string }
								ssh.Unmarshal(req.Payload, &p)
								req.Reply(true, nil)
								switch p.Command {
								case "exit7":
									io.WriteString(channel.Stderr(), "problem")
									channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{7}))
								case "flood":
									io.WriteString(channel, strings.Repeat("x", streamLimit+100))
									channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
								case "hardflood":
									io.WriteString(channel, strings.Repeat("x", totalOutputLimit+100000))
									return
								case "missing":
									return
								case "hang":
									io.Copy(io.Discard, channel)
									return
								default:
									io.WriteString(channel, "ok")
									channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
								}
								return
							}
						}()
					default:
						ch.Reject(ssh.UnknownChannelType, "unsupported")
					}
				}
			}()
		}
	}()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	portNumber, _ := strconv.Atoi(port)
	return TransportEndpoint{Address: host, Port: portNumber, Username: "test", Password: "password", HostKeys: []string{string(ssh.MarshalAuthorizedKey(signer.PublicKey()))}}, auths
}

func TestNativeTransportRoutes(t *testing.T) {
	for hops := 0; hops <= 3; hops++ {
		t.Run(strconv.Itoa(hops), func(t *testing.T) {
			var endpoints []TransportEndpoint
			for i := 0; i <= hops; i++ {
				ep, _ := testSSHServer(t)
				endpoints = append(endpoints, ep)
			}
			r, e := (NativeTransport{}).Run(context.Background(), TransportRequest{Endpoints: endpoints, Command: "echo ok"})
			if e != nil || r.ExitCode == nil || *r.ExitCode != 0 || string(r.Stdout) != "ok" {
				t.Fatalf("unexpected result: %+v %v", r, e)
			}
		})
	}
}

func TestNativeTransportHostTrustBeforeAuthentication(t *testing.T) {
	ep, auths := testSSHServer(t)
	other, _ := testSSHServer(t)
	ep.HostKeys = other.HostKeys
	r, e := (NativeTransport{}).Run(context.Background(), TransportRequest{Endpoints: []TransportEndpoint{ep}, Command: "echo ok"})
	if e == nil || r.Dispatched || auths.Load() != 0 {
		t.Fatalf("host trust bypass: %+v %v auth=%d", r, e, auths.Load())
	}
}

func TestNativeTransportJumpHostTrustBeforeAuthentication(t *testing.T) {
	jump, _ := testSSHServer(t)
	target, auths := testSSHServer(t)
	other, _ := testSSHServer(t)
	target.HostKeys = other.HostKeys
	r, e := (NativeTransport{}).Run(context.Background(), TransportRequest{Endpoints: []TransportEndpoint{jump, target}, Command: "echo ok"})
	if e == nil || r.Dispatched || auths.Load() != 0 {
		t.Fatalf("downstream host trust bypass: %+v %v auth=%d", r, e, auths.Load())
	}
}

func TestNativeTransportOutcomes(t *testing.T) {
	ep, _ := testSSHServer(t)
	for _, command := range []string{"exit7", "flood", "hardflood", "missing", "hang"} {
		t.Run(command, func(t *testing.T) {
			timeout := 3 * time.Second
			if command == "hang" {
				timeout = 200 * time.Millisecond
			}
			r, e := (NativeTransport{}).Run(context.Background(), TransportRequest{Endpoints: []TransportEndpoint{ep}, Command: command, Timeout: timeout})
			switch command {
			case "exit7":
				if e != nil || r.ExitCode == nil || *r.ExitCode != 7 || string(r.Stderr) != "problem" {
					t.Fatalf("%+v %v", r, e)
				}
			case "hardflood":
				if e == nil || r.ErrorCode != "output_limit_unknown_outcome" || !r.Truncated || r.ExitCode != nil {
					t.Fatalf("hard quota: %+v %v", r, e)
				}
			case "flood":
				if e != nil || !r.Truncated || len(r.Stdout) != streamLimit || r.ExitCode == nil {
					t.Fatalf("%+v %v", r, e)
				}
			default:
				if e == nil || r.ErrorCode != "unknown_outcome" || !r.Dispatched || r.ExitCode != nil {
					t.Fatalf("%+v %v", r, e)
				}
			}
		})
	}
}

func TestNativeTransportPrivateKey(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(key)
	ep, _ := testSSHServer(t, signer.PublicKey())
	block, e := ssh.MarshalPrivateKey(key, "")
	if e != nil {
		t.Fatal(e)
	}
	ep.Password = ""
	ep.PrivateKey = string(pem.EncodeToMemory(block))
	config, e := endpointConfig(ep)
	if e != nil || len(config.Auth) != 1 {
		t.Fatalf("key configuration: %v", e)
	}
	r, runErr := (NativeTransport{}).Run(context.Background(), TransportRequest{Endpoints: []TransportEndpoint{ep}, Command: "ok"})
	if runErr != nil || r.ExitCode == nil {
		t.Fatalf("key auth: %+v %v", r, runErr)
	}
	block, e = ssh.MarshalPrivateKeyWithPassphrase(key, "", []byte("passphrase"))
	if e != nil {
		t.Fatal(e)
	}
	ep.PrivateKey = string(pem.EncodeToMemory(block))
	ep.Passphrase = "passphrase"
	if _, e = endpointConfig(ep); e != nil {
		t.Fatal(e)
	}
	r, runErr = (NativeTransport{}).Run(context.Background(), TransportRequest{Endpoints: []TransportEndpoint{ep}, Command: "ok"})
	if runErr != nil || r.ExitCode == nil {
		t.Fatalf("encrypted key auth: %+v %v", r, runErr)
	}
	ep.Passphrase = "wrong"
	if _, e = endpointConfig(ep); e == nil {
		t.Fatal("wrong passphrase accepted")
	}
}

func TestNativeTransportCancelHandshake(t *testing.T) {
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	go func() {
		c, acceptErr := listener.Accept()
		if acceptErr == nil {
			defer c.Close()
			io.Copy(io.Discard, c)
		}
	}()
	ep, _ := testSSHServer(t)
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	ep.Address = host
	ep.Port, _ = strconv.Atoi(port)
	start := time.Now()
	r, e := (NativeTransport{}).Run(context.Background(), TransportRequest{Endpoints: []TransportEndpoint{ep}, Command: "x", Timeout: 50 * time.Millisecond})
	if e == nil || r.Dispatched || time.Since(start) > time.Second {
		t.Fatalf("cancellation failed: %+v %v", r, e)
	}
}

func TestNativeTransportCancelDownstreamHandshake(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	closed := make(chan struct{})
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			defer connection.Close()
			io.Copy(io.Discard, connection)
		}
		close(closed)
	}()
	jump, _ := testSSHServer(t)
	target, _ := testSSHServer(t)
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	target.Address = host
	target.Port, _ = strconv.Atoi(port)
	result, err := (NativeTransport{}).Run(context.Background(), TransportRequest{Endpoints: []TransportEndpoint{jump, target}, Command: "x", Timeout: 150 * time.Millisecond})
	if err == nil || result.Dispatched {
		t.Fatalf("downstream handshake cancellation: %+v %v", result, err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("cancellation leaked downstream TCP connection")
	}
}

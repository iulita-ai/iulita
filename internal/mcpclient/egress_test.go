package mcpclient

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPinnedProxyBlocksDNSRebindingAndPreservesTLS(t *testing.T) {
	for _, secureProxy := range []bool{false, true} {
		t.Run(map[bool]string{false: "HTTP", true: "HTTPS"}[secureProxy], func(t *testing.T) {
			var mutations atomic.Int32
			target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Host != "example.com" || r.TLS.ServerName != "example.com" {
					t.Errorf("original identity lost: Host=%s SNI=%s", r.Host, r.TLS.ServerName)
				}
				if r.URL.Path == "/mutation" {
					_, _ = io.Copy(io.Discard, r.Body)
					mutations.Add(1)
					connection, _, err := w.(http.Hijacker).Hijack()
					if err == nil {
						_ = connection.Close()
					}
					return
				}
				_, _ = w.Write([]byte("fixture"))
			}))
			defer target.Close()
			var connects atomic.Int32
			proxy := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				connects.Add(1)
				if r.Method != "CONNECT" || r.Host != "1.1.1.1:443" {
					t.Errorf("proxy received unpinned destination %s %s", r.Method, r.Host)
					w.WriteHeader(403)
					return
				}
				upstream, e := net.DialTimeout("tcp", target.Listener.Addr().String(), time.Second)
				if e != nil {
					t.Error(e)
					w.WriteHeader(502)
					return
				}
				defer upstream.Close()
				conn, _, e := w.(http.Hijacker).Hijack()
				if e != nil {
					t.Error(e)
					return
				}
				defer conn.Close()
				_, _ = conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
				go func() { _, _ = io.Copy(upstream, conn) }()
				_, _ = io.Copy(conn, upstream)
			}))
			if secureProxy {
				key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				template := *target.Certificate()
				template.SignatureAlgorithm = x509.ECDSAWithSHA256
				template.PublicKey = &key.PublicKey
				template.PublicKeyAlgorithm = x509.ECDSA
				template.DNSNames = nil
				template.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
				der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
				if err != nil {
					t.Fatal(err)
				}
				proxy.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
				proxy.StartTLS()
			} else {
				proxy.Start()
			}
			defer proxy.Close()
			protocol, e := NewSDKProtocol(proxy.URL)
			if e != nil {
				t.Fatal(e)
			}
			defer protocol.Close()
			p := protocol.Transport.(*pinnedProxy)
			roots := x509.NewCertPool()
			roots.AddCert(target.Certificate())
			if secureProxy {
				roots.AddCert(proxy.Certificate())
			}
			p.base.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
			var lookupCount atomic.Int32
			p.lookup = func(context.Context, string) ([]net.IPAddr, error) {
				ip := "1.1.1.1"
				if lookupCount.Add(1) > 1 {
					ip = "127.0.0.1"
				}
				return []net.IPAddr{{IP: net.ParseIP(ip)}}, nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			request, _ := http.NewRequestWithContext(ctx, "GET", "https://example.com/result", http.NoBody)
			response, e := p.RoundTrip(request)
			if e != nil {
				t.Fatal(e)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if _, e = p.RoundTrip(request); !errors.Is(e, ErrDenied) {
				t.Fatal("rebound private answer admitted", e)
			}
			if connects.Load() != 1 {
				t.Fatal("private destination contacted proxy", connects.Load())
			}
			p.lookup = func(context.Context, string) ([]net.IPAddr, error) {
				return []net.IPAddr{{IP: net.ParseIP("1.1.1.1")}}, nil
			}
			mutation, _ := http.NewRequestWithContext(ctx, "POST", "https://example.com/mutation", strings.NewReader(`{"fixture":true}`))
			mutation.Header.Set("X-Idempotency-Key", "synthetic-retry-canary")
			lost, mutationErr := p.RoundTrip(mutation)
			if lost != nil {
				_ = lost.Body.Close()
			}
			if mutationErr == nil || mutations.Load() != 1 {
				t.Fatalf("lost response replayed a mutating request: calls=%d error=%v", mutations.Load(), mutationErr)
			}
			wrong, _ := http.NewRequestWithContext(ctx, "GET", "https://wrong.example/result", http.NoBody)
			response, err := p.RoundTrip(wrong)
			if response != nil {
				_ = response.Body.Close()
			}
			var hostnameError x509.HostnameError
			if !errors.As(err, &hostnameError) {
				t.Fatalf("destination certificate identity was not enforced: %v", err)
			}
		})
	}

}

func TestPinnedProxyRejectsSpecialAndMixedAnswersBeforeProxy(t *testing.T) {
	var connects atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { connects.Add(1); w.WriteHeader(502) }))
	defer proxy.Close()
	protocol, e := NewSDKProtocol(proxy.URL)
	if e != nil {
		t.Fatal(e)
	}
	defer protocol.Close()
	p := protocol.Transport.(*pinnedProxy)
	for _, ip := range []string{"127.0.0.1", "10.43.0.1", "169.254.169.254", "100.64.0.1", "168.63.129.16", "192.0.2.1", "::1", "::ffff:127.0.0.1", "::192.168.1.1", "::ffff:0:192.168.1.1", "64:ff9b::a00:1", "64:ff9b:1::a00:1", "2002:0a00:0001::1", "fec0::1"} {
		t.Run(ip, func(t *testing.T) {
			p.lookup = func(context.Context, string) ([]net.IPAddr, error) {
				return []net.IPAddr{{IP: net.ParseIP("1.1.1.1")}, {IP: net.ParseIP(ip)}}, nil
			}
			request, _ := http.NewRequestWithContext(context.Background(), "GET", "https://fixture.example/mcp", http.NoBody)
			if _, e := p.RoundTrip(request); !errors.Is(e, ErrDenied) {
				t.Fatal(e)
			}
		})
	}
	if connects.Load() != 0 {
		t.Fatal("blocked answer reached proxy")
	}
}

// Opt-in, read-only probes through an existing deployment proxy. No credentials,
// cookies, private headers or MCP invocation arguments are sent by this test.
func TestLiveProxyPinnedDestinations(t *testing.T) {
	proxyURL := os.Getenv("IULITA_MCP_AUDIT_PROXY")
	if proxyURL == "" {
		t.Skip("set IULITA_MCP_AUDIT_PROXY for live proxy verification")
	}
	protocol, e := NewSDKProtocol(proxyURL)
	if e != nil {
		t.Fatal(e)
	}
	defer protocol.Close()
	client := &http.Client{Transport: protocol.Transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrDenied }}
	for _, endpoint := range []string{"https://127.0.0.1/", "https://10.43.0.1/", "https://169.254.169.254/", "https://[::1]/", "https://[::ffff:127.0.0.1]/", "https://127.0.0.1.sslip.io/", "https://10.43.0.1.sslip.io/", "https://169.254.169.254.sslip.io/"} {
		t.Run(endpoint, func(t *testing.T) {
			response, probeErr := client.Get(endpoint)
			if response != nil {
				_ = response.Body.Close()
			}
			if !errors.Is(probeErr, ErrDenied) {
				t.Fatalf("private destination not rejected: %v", probeErr)
			}
		})
	}
	response, e := client.Get("https://example.com/")
	if e != nil {
		t.Fatal(e)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}
	t.Log("public HTTPS reached through fixed proxy with original hostname verification")
	if endpoint := os.Getenv("IULITA_MCP_AUDIT_ENDPOINT"); strings.HasPrefix(endpoint, "https://") {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		items, e := protocol.Discover(ctx, Connection{ID: "live-audit", OwnerID: "synthetic-audit", Transport: "http", Endpoint: endpoint, Generation: 1})
		if e != nil {
			t.Fatal(e)
		}
		t.Logf("public MCP catalog discovered: %d capabilities; no capability invoked", len(items))
	}
}

func TestPinnedSOCKSUsesNumericDestination(t *testing.T) {
	for _, scheme := range []string{"socks5", "socks5h"} {
		t.Run(scheme, func(t *testing.T) {
			target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Host != "example.com" || r.TLS.ServerName != "example.com" {
					t.Error("lost target identity")
				}
				w.WriteHeader(204)
			}))
			defer target.Close()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			var numeric atomic.Int32
			go func() {
				connection, socksErr := listener.Accept()
				if socksErr != nil {
					return
				}
				defer connection.Close()
				if socksErr = connection.SetDeadline(time.Now().Add(5 * time.Second)); socksErr != nil {
					return
				}
				greeting := make([]byte, 2)
				if _, socksErr = io.ReadFull(connection, greeting); socksErr != nil || greeting[0] != 5 {
					return
				}
				methods := make([]byte, int(greeting[1]))
				if _, socksErr = io.ReadFull(connection, methods); socksErr != nil {
					return
				}
				if _, socksErr = connection.Write([]byte{5, 0}); socksErr != nil {
					return
				}
				request := make([]byte, 10)
				if _, socksErr = io.ReadFull(connection, request); socksErr != nil {
					return
				}
				if !bytes.Equal(request, []byte{5, 1, 0, 1, 1, 1, 1, 1, 1, 187}) {
					t.Errorf("SOCKS received nonnumeric/unexpected destination %v", request)
					return
				}
				numeric.Add(1)
				upstream, socksErr := net.DialTimeout("tcp", target.Listener.Addr().String(), time.Second)
				if socksErr != nil {
					return
				}
				defer upstream.Close()
				if _, socksErr = connection.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); socksErr != nil {
					return
				}
				go func() { _, _ = io.Copy(upstream, connection) }()
				_, _ = io.Copy(connection, upstream)
			}()
			protocol, err := NewSDKProtocol(scheme + "://" + listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer protocol.Close()
			pinned := protocol.Transport.(*pinnedProxy)
			roots := x509.NewCertPool()
			roots.AddCert(target.Certificate())
			pinned.base.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
			pinned.lookup = func(context.Context, string) ([]net.IPAddr, error) {
				return []net.IPAddr{{IP: net.ParseIP("1.1.1.1")}}, nil
			}
			client := &http.Client{Transport: pinned, Timeout: 5 * time.Second}
			response, err := client.Get("https://example.com/")
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != 204 || numeric.Load() != 1 {
				t.Fatal("SOCKS pinning failed")
			}
		})
	}
}

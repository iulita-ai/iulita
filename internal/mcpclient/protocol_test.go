package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type routeTransport struct {
	target  *url.URL
	base    http.RoundTripper
	mu      sync.Mutex
	headers []http.Header
	caps    []json.RawMessage
}

func (t *routeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	q := r.Clone(r.Context())
	u := *r.URL
	u.Scheme = t.target.Scheme
	u.Host = t.target.Host
	q.URL = &u
	q.Host = t.target.Host
	t.mu.Lock()
	t.headers = append(t.headers, r.Header.Clone())
	if r.Body != nil {
		raw, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(raw))
		q.Body = io.NopCloser(bytes.NewReader(raw))
		var wire struct {
			Method string `json:"method"`
			Params struct {
				Capabilities json.RawMessage            `json:"capabilities"`
				Meta         map[string]json.RawMessage `json:"_meta"`
			} `json:"params"`
		}
		_ = json.Unmarshal(raw, &wire)
		if wire.Method == "initialize" {
			t.caps = append(t.caps, wire.Params.Capabilities)
		}
		if c := wire.Params.Meta["io.modelcontextprotocol/clientCapabilities"]; len(c) > 0 {
			t.caps = append(t.caps, c)
		}
	}

	t.mu.Unlock()
	return t.base.RoundTrip(q)
}
func TestSDKProtocolWireProfiles(t *testing.T) {
	for _, modern := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "modern"}[modern], func(t *testing.T) {
			var caps json.RawMessage
			var sessionIDs []string
			var mu sync.Mutex
			server := sdk.NewServer(&sdk.Implementation{Name: "fixture", Version: "1"}, nil)
			server.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
				return func(ctx context.Context, method string, r sdk.Request) (sdk.Result, error) {
					if method == "initialize" {
						b, _ := json.Marshal(r.GetParams())
						var v map[string]json.RawMessage
						_ = json.Unmarshal(b, &v)
						mu.Lock()
						caps = v["capabilities"]
						mu.Unlock()
					}
					return next(ctx, method, r)
				}
			})
			type args struct {
				Message string `json:"message"`
			}
			sdk.AddTool(server, &sdk.Tool{Name: "echo", Description: "fixture echo"}, func(_ context.Context, req *sdk.CallToolRequest, in args) (*sdk.CallToolResult, any, error) {
				mu.Lock()
				sessionIDs = append(sessionIDs, req.Session.ID())
				mu.Unlock()
				return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: in.Message}}}, nil, nil
			})
			handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{Stateless: modern})
			httpServer := httptest.NewServer(handler)
			defer httpServer.Close()
			target, _ := url.Parse(httpServer.URL)
			tr := &routeTransport{target: target, base: httpServer.Client().Transport}
			protocol := &SDKProtocol{Transport: tr}
			defer protocol.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			connection := Connection{Transport: "http", Endpoint: "https://fixture.example/mcp", Token: "TOKEN-CANARY"} // # gitleaks:allow synthetic wire-test canary, no credentials
			catalog, e := protocol.Discover(ctx, connection)
			if e != nil || len(catalog) != 1 {
				t.Fatalf("catalog %v %v", catalog, e)
			}
			if catalog[0].ErrorCode != "" {
				t.Fatal(catalog[0])
			}
			dispatches := 0
			raw, isError, e := protocol.Invoke(ctx, connection, catalog[0], json.RawMessage(`{"message":"hello"}`), func() error { dispatches++; return nil })
			if e != nil || isError || dispatches != 1 {
				t.Fatal(string(raw), isError, dispatches, e)
			}
			text, _ := project(raw)
			if text != "hello" {
				t.Fatal(string(raw))
			}
			_, _, e = protocol.Invoke(ctx, connection, catalog[0], json.RawMessage(`{"message":"second"}`), func() error { return nil })
			if e != nil {
				t.Fatal(e)
			}
			mu.Lock()
			defer mu.Unlock()
			if !modern && (len(sessionIDs) != 2 || sessionIDs[0] == "" || sessionIDs[0] != sessionIDs[1]) {
				t.Fatalf("legacy session state was not preserved: %v", sessionIDs)
			}
			_ = caps
			tr.mu.Lock()
			defer tr.mu.Unlock()
			for _, wireCaps := range tr.caps {
				if string(wireCaps) != "{}" {
					t.Fatalf("wire capabilities %s", wireCaps)
				}
			}
			if len(tr.caps) == 0 {
				t.Fatal("no wire capabilities captured")
			}
			for _, h := range tr.headers {
				if h.Get("Authorization") != "Bearer TOKEN-CANARY" {
					t.Fatal("missing bound auth")
				}
				if h.Get("Mcp-Method") == "tools/call" && h.Get("Mcp-Name") != "echo" {
					t.Fatal("missing modern name header")
				}
			}
		})
	}
}
func TestEndpointAndInputGuards(t *testing.T) {
	for _, s := range []string{"http://example.org/mcp", "https://localhost/mcp", "https://127.0.0.1/", "https://[::1]/", "https://169.254.169.254/", "https://example.org:8443/", "https://x:y@example.org/", "https://example.org/?token=secret"} {
		if _, e := validateEndpoint(s); e == nil {
			t.Fatal(s)
		}
	}
	for _, s := range []string{`{"a":1,"a":2}`, `{"x":{"a":1,"a":2}}`} {
		if validJSON([]byte(s), 1024) == nil {
			t.Fatal(s)
		}
	}
	if _, e := schema(json.RawMessage(`{"$ref":"https://169.254.169.254/schema"}`)); e == nil {
		t.Fatal("external schema admitted")
	}
}

func TestMCPUsesMandatoryProxyWithoutEnvironmentBypass(t *testing.T) {
	var mu sync.Mutex
	connects := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		connects++
		mu.Unlock()
		if r.Method != "CONNECT" || r.Host != "1.1.1.1:443" {
			t.Errorf("unexpected proxy destination %s %s", r.Method, r.Host)
		}
		http.Error(w, "blocked", http.StatusForbidden)
	}))
	defer proxy.Close()
	t.Setenv("NO_PROXY", "*")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	protocol, e := NewSDKProtocol(proxy.URL)
	if e != nil {
		t.Fatal(e)
	}
	protocol.Transport.(*pinnedProxy).lookup = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("1.1.1.1")}}, nil
	}
	defer protocol.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, e = protocol.Discover(ctx, Connection{Transport: "http", Endpoint: "https://fixture.example/mcp"})
	if e == nil {
		t.Fatal("proxy block ignored")
	}
	mu.Lock()
	defer mu.Unlock()
	if connects == 0 {
		t.Fatal("fixed proxy bypassed")
	}
	if _, e = NewSDKProtocol(""); e == nil {
		t.Fatal("missing proxy accepted")
	}
}

func TestUnsupportedMRTRIsNotAutomaticallyContinued(t *testing.T) {
	var calls atomic.Int32
	server := sdk.NewServer(&sdk.Implementation{Name: "fixture", Version: "1"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "requires_roots"}, func(_ context.Context, _ *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, any, error) {
		calls.Add(1)
		//nolint:staticcheck // SA1019: negative compatibility fixture verifies deprecated roots are never serviced.
		return &sdk.CallToolResult{InputRequests: sdk.InputRequestMap{"roots": &sdk.ListRootsParams{}}}, nil, nil
	})
	httpServer := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{Stateless: true}))
	defer httpServer.Close()
	target, _ := url.Parse(httpServer.URL)
	protocol := &SDKProtocol{Transport: &routeTransport{target: target, base: httpServer.Client().Transport}}
	defer protocol.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connection := Connection{Transport: "http", Endpoint: "https://fixture.example/mcp"}
	items, e := protocol.Discover(ctx, connection)
	if e != nil || len(items) != 1 {
		t.Fatal(items, e)
	}
	_, _, e = protocol.Invoke(ctx, connection, items[0], json.RawMessage(`{}`), func() error { return nil })
	if e == nil || calls.Load() != 1 {
		t.Fatal("unsupported input was retried or reported complete", e, calls.Load())
	}
}

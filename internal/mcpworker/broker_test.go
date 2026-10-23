package mcpworker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestWorkerProfilesIsolationAndReplay(t *testing.T) {
	image := "registry.example/mcp@sha256:" + strings.Repeat("a", 64)
	b, e := NewBroker(make([]byte, 32), map[string]Profile{"fixture": {Image: image}}, filepath.Join(t.TempDir(), "nonces"))
	if e != nil {
		t.Fatal(e)
	}
	l := Lease{Owner: "owner", Connection: "connection", Generation: 1, Profile: "fixture", Nonce: "once", Expires: time.Now().Add(time.Minute).Unix()}
	raw, _ := json.Marshal(l)
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	request := httptest.NewRequest("GET", "https://worker.example/stdio", http.NoBody)
	request.Header.Set("X-Iulita-MCP-Lease", encoded)
	request.Header.Set("X-Iulita-MCP-Signature", signature(b.Key, encoded))
	if _, _, e = b.verify(request); e != nil {
		t.Fatal(e)
	}
	if _, _, e = b.verify(request); e == nil {
		t.Fatal("lease replay accepted")
	}
	forged := request.Clone(context.Background())
	forged.Header = forged.Header.Clone()
	forged.Header.Set("X-Iulita-MCP-Signature", "forged")
	if _, _, e = b.verify(forged); e == nil {
		t.Fatal("forged lease")
	}
	for _, p := range []Profile{{Image: "npm:latest"}, {Image: image, Network: "host"}, {Image: image, Network: "bridge"}} {
		if validateProfile(p) == nil {
			t.Fatal("unsafe profile", p)
		}
	}
	args := strings.Join(PodmanArgs(Profile{Image: image}, "fixture", true), " ")
	for _, required := range []string{"--pull=never", "--read-only", "--network none", "--user 65532:65532", "--cap-drop ALL", "--security-opt no-new-privileges", "--pids-limit 64", "--env MCP_ACCESS_TOKEN"} {
		if !strings.Contains(args, required) {
			t.Fatal(args)
		}
	}
	for _, bad := range []string{"--privileged", "--volume", "--mount", "--network host"} {
		if strings.Contains(args, bad) {
			t.Fatal(args)
		}
	}
	t.Setenv("IULITA_MASTER_KEY", "HOST-SECRET-CANARY")
	t.Setenv("MCP_ACCESS_TOKEN", "INHERITED-TOKEN-CANARY")
	for _, env := range minimalEnv() {
		if strings.Contains(env, "CANARY") {
			t.Fatal("secret inherited")
		}
	}
	browser := httptest.NewRequest("GET", "https://worker.example/stdio", http.NoBody)
	browser.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	b.ServeHTTP(w, browser)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	if info, e := os.Stat(b.StateDir); e != nil || info.Mode().Perm() != 0o700 {
		t.Fatal(info, e)
	}
}
func TestWorkerClientRejectsPlaintextAndCredentialsInURL(t *testing.T) {
	for _, endpoint := range []string{"ws://worker.example/stdio", "wss://user:password@worker.example/", "wss://worker.example/?token=secret"} {
		if _, e := NewClient(endpoint, make([]byte, 32)); e == nil {
			t.Fatal(endpoint)
		}
	}
	if _, e := NewClient("unix:///tmp/mcp-worker.sock", make([]byte, 32)); e != nil {
		t.Fatal(e)
	}
}

// TestWorkerProcess is a controlled stdio MCP peer launched only by the bridge
// test. It proves real process framing/cleanup, not Podman kernel isolation.
func TestWorkerProcess(t *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-1] != "mcp-test-helper" {
		return
	}
	server := sdk.NewServer(&sdk.Implementation{Name: "worker-fixture", Version: "1"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "environment_check"}, func(_ context.Context, _ *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, any, error) {
		safe := os.Getenv("MCP_ACCESS_TOKEN") == "AUTHORIZED-TOKEN" && os.Getenv("IULITA_MASTER_KEY") == ""
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: fmt.Sprint(safe)}}}, nil, nil
	})
	_ = server.Run(context.Background(), &sdk.StdioTransport{MaxLineLength: 8 << 20})
	os.Exit(0)
}
func TestSignedBrokerToRealStdioProcess(t *testing.T) {
	key := make([]byte, 32)
	b, e := NewBroker(key, map[string]Profile{"fixture": {Image: "fixture@sha256:" + strings.Repeat("a", 64)}}, filepath.Join(t.TempDir(), "state"))
	if e != nil {
		t.Fatal(e)
	}
	b.command = func(ctx context.Context, _ []string) *exec.Cmd {
		return exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWorkerProcess$", "--", "mcp-test-helper")
	}
	t.Setenv("IULITA_MASTER_KEY", "HOST-SECRET-CANARY")
	httpServer := httptest.NewTLSServer(b)
	defer httpServer.Close()
	client, e := NewClient("wss"+strings.TrimPrefix(httpServer.URL, "https"), key)
	if e != nil {
		t.Fatal(e)
	}
	client.Dialer.TLSClientConfig = httpServer.Client().Transport.(*http.Transport).TLSClientConfig
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	transport, e := client.Transport(ctx, "owner", "connection", 1, "fixture", "AUTHORIZED-TOKEN")
	if e != nil {
		t.Fatal(e)
	}
	sdkClient := sdk.NewClient(&sdk.Implementation{Name: "client", Version: "1"}, &sdk.ClientOptions{Capabilities: &sdk.ClientCapabilities{}, MultiRoundTrip: &sdk.MultiRoundTripOptions{Disabled: true}})
	session, e := sdkClient.Connect(ctx, transport, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer session.Close()
	result, e := session.CallTool(ctx, &sdk.CallToolParams{Name: "environment_check", Arguments: map[string]any{}})
	if e != nil || len(result.Content) != 1 {
		t.Fatal(result, e)
	}
	if result.Content[0].(*sdk.TextContent).Text != "true" {
		t.Fatal("worker inherited ambient secrets or lost authorized credential")
	}
}

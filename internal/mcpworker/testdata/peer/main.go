// A disposable, network-free MCP peer for opt-in Podman isolation tests.
package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func read(path string) string {
	b, e := os.ReadFile(path) //nolint:gosec // G304: fixture callers supply only fixed /proc and /sys isolation paths.
	if e != nil {
		return "unavailable"
	}
	return strings.TrimSpace(string(b))
}
func main() {
	if len(os.Args) > 1 && os.Args[1] == "--check-hold" {
		if _, err := os.Stat("/tmp/hold-started"); err != nil {
			os.Exit(1)
		}
		return
	}
	linger := len(os.Args) > 1 && os.Args[1] == "--linger"
	server := sdk.NewServer(&sdk.Implementation{Name: "podman-isolation-peer", Version: "1"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "probe_isolation", Description: "Returns only synthetic isolation evidence"}, func(_ context.Context, _ *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, any, error) {
		status := read("/proc/self/status")
		fields := map[string]string{}
		for _, line := range strings.Split(status, "\n") {
			key, value, ok := strings.Cut(line, ":")
			if ok && (key == "CapEff" || key == "NoNewPrivs" || key == "Seccomp") {
				fields[key] = strings.TrimSpace(value)
			}
		}
		rootRO := false
		for _, line := range strings.Split(read("/proc/self/mountinfo"), "\n") {
			f := strings.Fields(line)
			if len(f) > 5 && f[4] == "/" {
				rootRO = strings.Contains(","+f[5]+",", ",ro,")
			}
		}
		conn, networkErr := net.DialTimeout("tcp", "1.1.1.1:443", time.Second)
		if conn != nil {
			if closeErr := conn.Close(); closeErr != nil {
				return nil, nil, closeErr
			}
		}
		temporary, tmpErr := os.CreateTemp("/tmp", "writable-probe-")
		if tmpErr == nil {
			_, tmpErr = temporary.WriteString("fixture")
			if closeErr := temporary.Close(); closeErr != nil {
				return nil, nil, closeErr
			}
		}
		result := map[string]any{"uid": os.Getuid(), "gid": os.Getgid(), "root_readonly": rootRO, "tmp_writable": tmpErr == nil, "public_network_blocked": networkErr != nil, "authorized_token_present": os.Getenv("MCP_ACCESS_TOKEN") == "AUTHORIZED-CANARY", "ambient_secret_absent": os.Getenv("IULITA_MASTER_KEY") == "", "security": fields, "pids_max": read("/sys/fs/cgroup/pids.max"), "memory_max": read("/sys/fs/cgroup/memory.max"), "cpu_max": read("/sys/fs/cgroup/cpu.max")}
		b, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			return nil, nil, marshalErr
		}
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: string(b)}}}, nil, nil
	})
	sdk.AddTool(server, &sdk.Tool{Name: "hold"}, func(ctx context.Context, _ *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, any, error) {
		//nolint:gosec // G303: stable marker in the disposable container's private tmpfs, observed by the test.
		if writeErr := os.WriteFile("/tmp/hold-started", []byte("fixture"), 0o600); writeErr != nil {
			return nil, nil, writeErr
		}
		<-ctx.Done()
		return nil, nil, ctx.Err()
	})
	if runErr := server.Run(context.Background(), &sdk.StdioTransport{MaxLineLength: 8 << 20}); runErr != nil && !linger {
		os.Exit(1)
	}
	if linger {
		for {
			time.Sleep(time.Hour)
		}
	}
}

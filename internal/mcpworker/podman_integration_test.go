package mcpworker

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// These tests run inside an isolated Linux VM as a non-root user. They use the
// actual worker executable and Podman; never the injected test command launcher.
// The fixed synthetic peer is built from testdata/peer and pinned by digest.
type podmanAuditWorker struct {
	cmd    *exec.Cmd
	once   sync.Once
	err    error
	socket string
	key    []byte
}

func (w *podmanAuditWorker) stop(force bool) error {
	w.once.Do(func() {
		if force {
			_ = w.cmd.Process.Kill()
		} else {
			_ = w.cmd.Process.Signal(syscall.SIGTERM)
		}
		w.err = w.cmd.Wait()
	})
	return w.err
}
func podmanOutput(t *testing.T, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "podman", args...).Output()
	if err != nil {
		t.Fatalf("podman %s: %v", args[0], err)
	}
	return out
}
func podmanAuditStart(t *testing.T, linger bool) *podmanAuditWorker {
	t.Helper()
	image, binary := os.Getenv("IULITA_MCP_PODMAN_IMAGE"), os.Getenv("IULITA_MCP_WORKER_BINARY")
	if image == "" || binary == "" {
		t.Skip("set IULITA_MCP_PODMAN_IMAGE and IULITA_MCP_WORKER_BINARY inside a rootless Podman VM")
	}
	if os.Geteuid() == 0 {
		t.Fatal("real isolation verification must run as non-root")
	}
	if got := strings.TrimSpace(string(podmanOutput(t, "info", "--format", "{{.Host.Security.Rootless}}"))); got != "true" {
		t.Fatal("not rootless", got)
	}
	if got := strings.TrimSpace(string(podmanOutput(t, "ps", "-a", "--filter", "name=iulita-mcp-", "--format", "{{.ID}}"))); got != "" {
		t.Fatal("audit requires no pre-existing worker containers")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	profile := Profile{Image: image}
	if linger {
		profile.Args = []string{"--linger"}
	}
	if err := validateProfile(profile); err != nil {
		t.Fatal(err)
	}
	profiles, _ := json.Marshal(map[string]Profile{"fixture": profile})
	for name, data := range map[string][]byte{"key": key, "profiles.json": profiles} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	socket := filepath.Join(dir, "worker.sock")
	cmd := exec.Command(binary, "--socket", socket, "--key-file", filepath.Join(dir, "key"), "--profiles", filepath.Join(dir, "profiles.json"), "--state-dir", filepath.Join(dir, "nonces"))
	cmd.Env = append(os.Environ(), "IULITA_MASTER_KEY=HOST-CANARY")
	logFile, err := os.Create(filepath.Join(dir, "worker.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	w := &podmanAuditWorker{cmd: cmd, socket: socket, key: key}
	t.Cleanup(func() { _ = w.stop(true); _ = logFile.Close() })
	deadline := time.Now().Add(10 * time.Second)
	for {
		if info, err := os.Stat(socket); err == nil {
			if info.Mode().Perm() != 0o600 {
				t.Fatal("worker socket is not private")
			}
			break
		}
		if time.Now().After(deadline) {
			b, _ := os.ReadFile(filepath.Join(dir, "worker.log"))
			t.Fatalf("worker did not start: %s", b)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return w
}
func podmanAuditSession(t *testing.T, ctx context.Context, w *podmanAuditWorker) *sdk.ClientSession {
	t.Helper()
	client, err := NewClient("unix://"+w.socket, w.key)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := client.Transport(ctx, "synthetic-owner", "synthetic-connection", 1, "fixture", "AUTHORIZED-CANARY")
	if err != nil {
		t.Fatal(err)
	}
	s, err := sdk.NewClient(&sdk.Implementation{Name: "podman-audit", Version: "1"}, &sdk.ClientOptions{Capabilities: &sdk.ClientCapabilities{}, MultiRoundTrip: &sdk.MultiRoundTripOptions{Disabled: true}}).Connect(ctx, transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func podmanAuditContainer(t *testing.T) string {
	t.Helper()
	id := strings.TrimSpace(string(podmanOutput(t, "ps", "--filter", "name=iulita-mcp-", "--format", "{{.ID}}")))
	if id == "" || strings.Contains(id, "\n") {
		t.Fatal("expected exactly one active worker container", id)
	}
	return id
}
func podmanAuditEmpty(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if strings.TrimSpace(string(podmanOutput(t, "ps", "-a", "--filter", "name=iulita-mcp-", "--format", "{{.ID}}"))) == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("worker left a container behind")
		}
		time.Sleep(100 * time.Millisecond)
	}
}
func TestRealPodmanIsolationCancellationAndShutdown(t *testing.T) {
	w := podmanAuditStart(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := podmanAuditSession(t, ctx, w)
	result, err := s.CallTool(ctx, &sdk.CallToolParams{Name: "probe_isolation", Arguments: map[string]any{}})
	if err != nil || len(result.Content) != 1 {
		t.Fatal(result, err)
	}
	var evidence struct {
		UID      int               `json:"uid"`
		GID      int               `json:"gid"`
		RootRO   bool              `json:"root_readonly"`
		Tmp      bool              `json:"tmp_writable"`
		Network  bool              `json:"public_network_blocked"`
		Token    bool              `json:"authorized_token_present"`
		NoSecret bool              `json:"ambient_secret_absent"`
		Security map[string]string `json:"security"`
		PIDs     string            `json:"pids_max"`
		Memory   string            `json:"memory_max"`
		CPU      string            `json:"cpu_max"`
	}
	text := result.Content[0].(*sdk.TextContent).Text
	if err = json.Unmarshal([]byte(text), &evidence); err != nil {
		t.Fatal(err)
	}
	t.Log("isolation evidence:", text)
	if evidence.UID != 65532 || evidence.GID != 65532 || !evidence.RootRO || !evidence.Tmp || !evidence.Network || !evidence.Token || !evidence.NoSecret || evidence.Security["CapEff"] != "0000000000000000" || evidence.Security["NoNewPrivs"] != "1" || evidence.Security["Seccomp"] != "2" || evidence.PIDs != "64" || evidence.Memory != "268435456" || evidence.CPU != "100000 100000" {
		t.Fatal("real container isolation does not match contract")
	}
	id := podmanAuditContainer(t)
	var inspected []struct {
		HostConfig struct {
			LogConfig   struct{ Type string }
			NetworkMode string
		}
		Mounts []json.RawMessage
	}
	if err = json.Unmarshal(podmanOutput(t, "inspect", id), &inspected); err != nil || len(inspected) != 1 {
		t.Fatal(err)
	}
	if inspected[0].HostConfig.LogConfig.Type != "none" || inspected[0].HostConfig.NetworkMode != "none" || len(inspected[0].Mounts) != 0 {
		t.Fatal("unexpected logging, network or mounts")
	}
	pending := make(chan error, 1)
	go func() {
		_, callErr := s.CallTool(ctx, &sdk.CallToolParams{Name: "hold", Arguments: map[string]any{}})
		pending <- callErr
	}()
	deadline := time.Now().Add(5 * time.Second)
	for exec.Command("podman", "exec", id, "/peer", "--check-hold").Run() != nil {
		if time.Now().After(deadline) {
			t.Fatal("hold did not start")
		}
		time.Sleep(100 * time.Millisecond)
	}
	cancel()
	select {
	case callErr := <-pending:
		if callErr == nil {
			t.Fatal("canceled operation reported success")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("call did not cancel")
	}
	podmanAuditEmpty(t)
	t.Log("cancellation removed the complete container")
	second, cancelSecond := context.WithCancel(context.Background())
	defer cancelSecond()
	_ = podmanAuditSession(t, second, w)
	_ = podmanAuditContainer(t)
	if err = w.stop(false); err != nil {
		t.Fatal(err)
	}
	podmanAuditEmpty(t)
	t.Log("SIGTERM waited for container cleanup and worker exit")
}
func TestRealPodmanIndependentCrashTimeout(t *testing.T) {
	w := podmanAuditStart(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	_ = podmanAuditSession(t, ctx, w)
	id := podmanAuditContainer(t)
	started := time.Now()
	if err := w.stop(true); err == nil {
		t.Fatal("broker was not killed")
	}
	time.Sleep(2 * time.Second)
	if strings.TrimSpace(string(podmanOutput(t, "inspect", "--format", "{{.State.Running}}", id))) != "true" {
		t.Fatal("linger fixture did not survive broker death")
	}
	t.Log("broker killed; container still running; observing independent 360s conmon timeout")
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("container outlived independent timeout")
		case <-ticker.C:
		}
		running := strings.TrimSpace(string(podmanOutput(t, "ps", "--filter", "id="+id, "--format", "{{.ID}}"))) != ""
		if !running {
			break
		}
		t.Logf("container still isolated after %s", time.Since(started).Round(time.Second))
	}
	elapsed := time.Since(started)
	if elapsed < 350*time.Second || elapsed > 390*time.Second {
		t.Fatal("unexpected independent lifetime", elapsed)
	}
	podmanAuditEmpty(t)
	t.Logf("independent timeout removed container after %s with broker absent", elapsed.Round(time.Second))
}

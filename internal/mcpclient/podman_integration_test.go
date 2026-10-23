package mcpclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/mcpworker"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// This opt-in test exercises the complete control plane, SQLite/vault, SDK,
// signed broker connection and a real digest-pinned rootless Podman container.
func TestRealPodmanServiceExecution(t *testing.T) {
	image := os.Getenv("IULITA_MCP_PODMAN_IMAGE")
	if image == "" {
		t.Skip("set IULITA_MCP_PODMAN_IMAGE inside the disposable rootless Podman VM")
	}
	if os.Geteuid() == 0 {
		t.Fatal("verification must run as non-root")
	}
	f := fixture(t)
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	broker, err := mcpworker.NewBroker(key, map[string]mcpworker.Profile{"fixture": {Image: image}}, filepath.Join(t.TempDir(), "nonces"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(broker)
	defer server.Close()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if shutdownErr := broker.Shutdown(ctx); shutdownErr != nil {
			t.Error(shutdownErr)
		}
	}()
	worker, err := mcpworker.NewClient("wss"+strings.TrimPrefix(server.URL, "https"), key)
	if err != nil {
		t.Fatal(err)
	}
	worker.Dialer.TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig
	protocol := &SDKProtocol{Stdio: func(ctx context.Context, c Connection) (sdk.Transport, error) {
		return worker.Transport(ctx, c.OwnerID, c.ID, c.Generation, c.Profile, c.Token)
	}}
	service, err := New(Options{Repository: f.repo, Vault: f.vault, Protocol: protocol, LockPath: filepath.Join(f.path, "mcp.lock"), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	t.Setenv("IULITA_MASTER_KEY", "HOST-CANARY")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	connection, err := service.Create(ctx, f.sc, Draft{Label: "Real Podman verification", Domain: "personal", Transport: "stdio", Profile: "fixture", ResultMode: "private"})
	if err != nil {
		t.Fatal(err)
	}
	if err = service.SetCredentials(ctx, f.sc, connection.ID, connection.Revision, "bearer", "AUTHORIZED-CANARY", nil); err != nil {
		t.Fatal(err)
	}
	connection, err = service.Connection(ctx, f.sc, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	connection, err = service.Action(ctx, f.sc, connection.ID, "connect", connection.Revision)
	if err != nil {
		t.Fatal(err)
	}
	var capability Item
	for _, item := range connection.Items {
		if item.Name == "probe_isolation" {
			capability = item
		}
	}
	if capability.ID == "" {
		t.Fatal("real peer catalog did not contain fixture tool")
	}
	if err = service.Review(ctx, f.sc, connection.ID, connection.Revision, Review{ItemID: capability.ID, Revision: capability.Revision, Effect: "read", Allowed: true}); err != nil {
		t.Fatal(err)
	}
	plan, err := service.Prepare(ctx, f.sc, Call{ConnectionID: connection.ID, ItemID: capability.ID, DescriptorRevision: capability.Revision, Arguments: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Approve(ctx, f.sc, plan.ID, plan.Hash, true); err != nil {
		t.Fatal(err)
	}
	result, err := service.Execute(ctx, f.sc, plan.ID)
	if err != nil || result.State != "succeeded" || result.ArtifactID == "" || result.Text != "" {
		t.Fatal(result, err)
	}
	artifact, err := service.Artifact(ctx, f.sc, result.ArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err = json.Unmarshal(artifact.Payload, &payload); err != nil || len(payload.Content) != 1 {
		t.Fatal(err)
	}
	var evidence struct {
		UID      int  `json:"uid"`
		Token    bool `json:"authorized_token_present"`
		NoSecret bool `json:"ambient_secret_absent"`
		Network  bool `json:"public_network_blocked"`
	}
	if err = json.Unmarshal([]byte(payload.Content[0].Text), &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.UID != 65532 || !evidence.Token || !evidence.NoSecret || !evidence.Network {
		t.Fatal("real control-plane isolation failed", evidence)
	}
	t.Log("exact owner approval -> durable execution -> real rootless container -> encrypted private artifact: succeeded; no result text delivered to model")
}

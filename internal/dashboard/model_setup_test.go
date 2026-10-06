package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/modelruntime"
	"github.com/iulita-ai/iulita/internal/models"
)

type setupProbeProvider struct{ model string }

func (p setupProbeProvider) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	r := llm.Response{Model: p.model, RequestedModel: p.model, ModelVerified: true, FinishReason: "stop", UsageReported: true}
	if len(req.ToolExchanges) > 0 {
		r.Content = req.ToolExchanges[0].Results[0].Content
		return r, nil
	}
	nonce := regexp.MustCompile(`[0-9a-f]{12}`).FindString(req.Message)
	raw, _ := json.Marshal(map[string]string{"nonce": nonce})
	if len(req.Tools) > 0 {
		r.ToolCalls = []llm.ToolCall{{ID: "echo", Name: "probe_echo", Input: raw}}
		r.FinishReason = "tool_calls"
	} else {
		r.Content = string(raw)
	}
	return r, nil
}
func TestModelsOnlyInstallFinishesSetupAndRequiresRestart(t *testing.T) {
	t.Setenv("IULITA_HOME", t.TempDir())
	cipher, _ := config.NewEncryptor(bytes.Repeat([]byte{7}, 32))
	manager, err := modelruntime.New(newMemConfigRepo(), cipher, func(p models.Profile, _ modelruntime.Connection) (llm.Provider, error) {
		return setupProbeProvider{p.Model}, nil
	}, func(context.Context, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	settings := models.Settings{SchemaVersion: 1, Profiles: []models.Profile{models.CandidateProfiles()[0]}}
	stage, err := manager.Stage(context.Background(), "admin", 0, settings, []modelruntime.ConnectionMutation{{Provider: "deepseek", APIKeyAction: "replace", APIKey: "synthetic-key"}}) // Synthetic test credential. gitleaks:allow
	if err != nil {
		t.Fatal(err)
	}
	for i, kind := range []string{"text", "tools"} {
		v, probeErr := manager.Probe(context.Background(), "admin", modelruntime.ProbeRequest{StageID: stage.ID, ProfileID: "ds-flash", Kind: kind, IdempotencyKey: strconv.FormatInt(time.Now().UnixMilli(), 10) + ":setupcheck-fixture-" + strconv.Itoa(i)})
		if probeErr != nil {
			t.Fatal(probeErr)
		}
		deadline := time.Now().Add(3 * time.Second)
		for v.Status == "running" && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
			v, probeErr = manager.ProbeStatus(context.Background(), "admin", v.ID)
			if probeErr != nil {
				t.Fatal(probeErr)
			}
		}
		if v.Result == nil || !v.Result.Passed {
			t.Fatalf("fixture probe failed: %+v", v)
		}
	}
	settings.Policy.Everyday = "ds-flash"
	settings.Policy.Background = "ds-flash"
	stage, err = manager.Stage(context.Background(), "admin", 0, settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Activate(context.Background(), "admin", 0, stage.ID); err != nil {
		t.Fatal(err)
	}
	cs := buildConfigStore(t, t.TempDir())
	s := buildWizardServer(t, cs)
	s.modelManager = manager
	s.setupMode = true
	cs.SetModelPolicyManaged()
	res, err := s.app.Test(httptest.NewRequest("GET", "/api/wizard/status", http.NoBody))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var status map[string]any
	_ = json.NewDecoder(res.Body).Decode(&status)
	if status["has_llm_provider"] != true || status["models_ready"] != true || status["model_restart_required"] != true {
		t.Fatalf("models-only setup not recognized: %+v", status)
	}
	res, err = s.app.Test(httptest.NewRequest("POST", "/api/wizard/complete", http.NoBody))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 {
		t.Fatalf("setup cannot finish: %d %s", res.StatusCode, raw)
	}
}

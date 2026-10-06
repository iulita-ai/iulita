package modelruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/models"
)

type memoryRepo struct {
	mu    sync.Mutex
	row   *domain.ConfigOverride
	fail  bool
	saves int
}

func (r *memoryRepo) GetConfigOverride(context.Context, string) (*domain.ConfigOverride, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.row == nil {
		return nil, nil
	}
	out := *r.row
	return &out, nil
}
func (r *memoryRepo) SaveConfigOverride(_ context.Context, row *domain.ConfigOverride) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail {
		return errors.New("private upstream failure")
	}
	out := *row
	r.row = &out
	r.saves++
	return nil
}
func (r *memoryRepo) setFailure(fail bool) { r.mu.Lock(); r.fail = fail; r.mu.Unlock() }

type providerFunc func(context.Context, llm.Request) (llm.Response, error)

func (p providerFunc) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	return p(ctx, req)
}

var noncePattern = regexp.MustCompile(`nonce[^a-zA-Z0-9]+([0-9a-f]{8,12})`)

func echoFactory(profile models.Profile, _ Connection) (llm.Provider, error) {
	return providerFunc(func(ctx context.Context, req llm.Request) (llm.Response, error) {
		if operationErr := ctx.Err(); operationErr != nil {
			return llm.Response{}, operationErr
		}
		response := llm.Response{Model: profile.Model, ModelVerified: true, RequestedModel: profile.Model, Usage: llm.Usage{InputTokens: 4, OutputTokens: 2}, FinishReason: "stop"}
		if len(req.Images) > 0 {
			var nonces, colors []string
			for _, attachment := range req.Images {
				nonce, fixtureColor, err := readFixture(attachment.Data)
				if err != nil {
					return llm.Response{}, err
				}
				nonces = append(nonces, nonce)
				colors = append(colors, fixtureColor)
			}
			var raw []byte
			if len(nonces) == 1 {
				raw, _ = json.Marshal(map[string]string{"nonce": nonces[0], "color": colors[0]})
			} else {
				raw, _ = json.Marshal(map[string][]string{"nonces": nonces, "colors": colors})
			}
			response.Content = string(raw)
			return response, nil
		}
		if len(req.ToolExchanges) > 0 {
			response.Content = req.ToolExchanges[0].Results[0].Content
			return response, nil
		}
		matches := noncePattern.FindStringSubmatch(req.Message)
		if len(matches) != 2 {
			return llm.Response{}, errors.New("test nonce was not provided")
		}
		nonce := matches[1]
		if len(req.Tools) > 0 {
			raw, _ := json.Marshal(map[string]string{"nonce": nonce})
			response.ToolCalls = []llm.ToolCall{{ID: "probe-call", Name: "probe_echo", Input: raw}}
			response.ReasoningContent = "opaque test reasoning"
			response.FinishReason = "tool_calls"
			return response, nil
		}
		raw, _ := json.Marshal(map[string]string{"nonce": nonce})
		response.Content = string(raw)
		return response, nil
	}), nil
}

// This small fixture-only OCR checks the actual raster attachment rather than
// taking the expected nonce from the text prompt, which contains no image label.
var fixtureTemplatesOnce sync.Once
var fixtureTemplates map[rune]image.Image

func readFixture(data []byte) (labelText, colorName string, err error) {
	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", "", err
	}
	red, _, blue, _ := decoded.At(80, 80).RGBA()
	name := "red"
	if blue > red {
		name = "blue"
	}
	fixtureTemplatesOnce.Do(func() {
		fixtureTemplates = map[rune]image.Image{}
		for _, candidate := range "0123456789abcdef" {
			fixtureTemplates[candidate] = imageFixtureCanvas(strings.Repeat(string(candidate), 8), imageColorRed())
		}
	})
	scale := decoded.Bounds().Dx() / 300
	var label strings.Builder
	for pos := 0; pos < 8; pos++ {
		matched := false
		for _, candidate := range "0123456789abcdef" {
			template := fixtureTemplates[candidate]
			equal := true
			for x := 110 + pos*7; x < 117+pos*7 && equal; x++ {
				for y := 30; y < 60; y++ {
					r, g, b, a := decoded.At(x*scale, y*scale).RGBA()
					tr, tg, tb, ta := template.At(x, y).RGBA()
					if r != tr || g != tg || b != tb || a != ta {
						equal = false
						break
					}
				}
			}
			if equal {
				label.WriteRune(candidate)
				matched = true
				break
			}
		}
		if !matched {
			return "", "", errors.New("fixture label not readable")
		}
	}
	return label.String(), name, nil
}
func imageColorRed() color.RGBA { return color.RGBA{R: 255, A: 255} }
func newTestManager(t *testing.T, repo *memoryRepo, factory Factory) *Manager {
	t.Helper()
	cipher, err := config.NewEncryptor(bytes.Repeat([]byte{42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(repo, cipher, factory, func(_ context.Context, actor string) error {
		if actor == "admin" || actor == "admin2" {
			return nil
		}
		return errors.New("forbidden")
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func draftSettings() models.Settings {
	p := models.CandidateProfiles()[0]
	return models.Settings{SchemaVersion: 1, Profiles: []models.Profile{p}, Policy: models.Policy{Everyday: p.ID}}
}
func stageDraft(t *testing.T, m *Manager, expected uint64, key string) StageView {
	t.Helper()
	stage, err := m.Stage(context.Background(), "admin", expected, draftSettings(), []ConnectionMutation{{Provider: "deepseek", APIKeyAction: "replace", APIKey: key}})
	if err != nil {
		t.Fatal(err)
	}
	return stage
}
func probeKey() string { return fmt.Sprintf("%d:%s", time.Now().UnixMilli(), randomID()) }
func waitProbe(t *testing.T, m *Manager, id string) ProbeView {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		v, err := m.ProbeStatus(context.Background(), "admin", id)
		if err != nil {
			t.Fatal(err)
		}
		if v.Status != "running" && v.Status != "cancel_requested" {
			return v
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("probe did not finish")
	return ProbeView{}
}
func checkStage(t *testing.T, m *Manager, stage StageView, kind string) ProbeView {
	t.Helper()
	v, err := m.Probe(context.Background(), "admin", ProbeRequest{ExpectedRevision: stage.BaseRevision, StageID: stage.ID, ProfileID: "ds-flash", Kind: kind, IdempotencyKey: probeKey()})
	if err != nil {
		t.Fatal(err)
	}
	v = waitProbe(t, m, v.ID)
	if v.Status != "completed" || v.Result == nil || !v.Result.Passed {
		t.Fatalf("probe failed: %+v", v)
	}
	return v
}
func activateDraft(t *testing.T, m *Manager, stage StageView) View {
	t.Helper()
	checkStage(t, m, stage, "text")
	checkStage(t, m, stage, "tools")
	view, err := m.Activate(context.Background(), "admin", stage.BaseRevision, stage.ID)
	if err != nil {
		t.Fatal(err)
	}
	return view
}
func assertCode(t *testing.T, err error, want string) {
	t.Helper()
	var coded *Error
	if !errors.As(err, &coded) || coded.Code != want {
		t.Fatalf("error=%v want=%s", err, want)
	}
}

func TestStageCommitFailureAndEncryptionFailClosed(t *testing.T) {
	repo := &memoryRepo{}
	m := newTestManager(t, repo, echoFactory)
	repo.setFailure(true)
	_, err := m.Stage(context.Background(), "admin", 0, draftSettings(), []ConnectionMutation{{Provider: "deepseek", APIKeyAction: "replace", APIKey: "secret-test-key"}}) // Synthetic test credential. gitleaks:allow
	assertCode(t, err, "storage_unavailable")
	if m.Snapshot().Stage != nil || len(m.Snapshot().Connections) != 0 {
		t.Fatal("failed save changed active state")
	}
	repo.setFailure(false)
	unencrypted, err := New(repo, nil, echoFactory, func(context.Context, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	_, err = unencrypted.Stage(context.Background(), "admin", 0, draftSettings(), nil)
	assertCode(t, err, "secret_encryption_unavailable")
	if repo.saves != 0 {
		t.Fatal("plaintext state was persisted")
	}
}
func TestStageMasksSecretsAndPreservesActiveKeyUntilVerifiedRotation(t *testing.T) {
	repo := &memoryRepo{}
	m := newTestManager(t, repo, echoFactory)
	stage := stageDraft(t, m, 0, "old-secret-key")
	view := activateDraft(t, m, stage)
	_, bindings, _, _ := m.Bindings()
	old := bindings["ds-flash"].Provider
	oldGen := view.Connections[0].Generation
	stage = stageDraft(t, m, 1, "replacement-secret-key")
	if m.Snapshot().Connections[0].Generation != oldGen {
		t.Fatal("stage rotated the active key")
	}
	if _, operationErr := old.Complete(context.Background(), llm.Request{Message: "nonce deadbeef0123"}); operationErr != nil {
		t.Fatalf("old key stopped during testing: %v", operationErr)
	}
	safe, _ := json.Marshal(m.Snapshot())
	if strings.Contains(string(safe), "secret-key") || strings.Contains(repo.row.Value, "secret-key") {
		t.Fatal("a secret escaped encryption or safe view")
	}
	if _, err := m.Activate(context.Background(), "admin", 1, stage.ID); err == nil {
		t.Fatal("unverified replacement activated")
	}
	checkStage(t, m, stage, "text")
	checkStage(t, m, stage, "tools")
	view, err := m.Activate(context.Background(), "admin", 1, stage.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Revision != 2 || view.Connections[0].Generation == oldGen {
		t.Fatal("rotation did not create a new generation")
	}
	_, err = old.Complete(context.Background(), llm.Request{Message: "nonce deadbeef0123"})
	assertCode(t, err, "credential_revoked")
	restarted := newTestManager(t, repo, echoFactory)
	if _, exists := restarted.state.Denied[oldGen]; !exists {
		t.Fatal("restart lost rotation deny")
	}
}
func TestStageActorRevisionCASAndDiscard(t *testing.T) {
	m := newTestManager(t, &memoryRepo{}, echoFactory)
	stage := stageDraft(t, m, 0, "secret-key")
	_, err := m.Activate(context.Background(), "admin2", 0, stage.ID)
	assertCode(t, err, "stage_owner_required")
	_, err = m.Stage(context.Background(), "admin2", 0, draftSettings(), nil)
	assertCode(t, err, "stage_busy")
	err = m.Discard(context.Background(), "admin", 1, stage.ID)
	assertCode(t, err, "revision_conflict")
	if operationErr := m.Discard(context.Background(), "admin", 0, stage.ID); operationErr != nil {
		t.Fatal(operationErr)
	}
	if m.Snapshot().Stage != nil {
		t.Fatal("discard kept pending identity")
	}
	_, err = m.Activate(context.Background(), "admin", 0, stage.ID)
	assertCode(t, err, "stage_conflict")
}
func TestRevokeCancelsAdmittedAndBlocksOldSnapshotAfterRestart(t *testing.T) {
	repo := &memoryRepo{}
	var admitted chan struct{}
	block := atomic.Bool{}
	factory := func(p models.Profile, c Connection) (llm.Provider, error) {
		normal, _ := echoFactory(p, c)
		return providerFunc(func(ctx context.Context, r llm.Request) (llm.Response, error) {
			if block.Load() {
				close(admitted)
				<-ctx.Done()
				return llm.Response{}, ctx.Err()
			}
			return normal.Complete(ctx, r)
		}), nil
	}
	m := newTestManager(t, repo, factory)
	view := activateDraft(t, m, stageDraft(t, m, 0, "secret-key"))
	_, bindings, _, _ := m.Bindings()
	old := bindings["ds-flash"].Provider
	admitted = make(chan struct{})
	block.Store(true)
	done := make(chan error, 1)
	go func() { _, err := old.Complete(context.Background(), llm.Request{Message: "blocked"}); done <- err }()
	<-admitted
	view, err := m.Revoke(context.Background(), "admin", view.Revision, "deepseek", view.Connections[0].Generation)
	if err != nil {
		t.Fatal(err)
	}
	if view.Health != "suspended" || len(view.AffectedRoles) != 1 || view.Revision != 2 {
		t.Fatalf("revoke health: %+v", view)
	}
	if completionErr := <-done; !errors.Is(completionErr, context.Canceled) {
		t.Fatalf("admitted attempt not canceled: %v", completionErr)
	}
	_, err = old.Complete(context.Background(), llm.Request{})
	assertCode(t, err, "credential_revoked")
	restarted := newTestManager(t, repo, echoFactory)
	_, bindings, _, _ = restarted.Bindings()
	_, err = bindings["ds-flash"].Provider.Complete(context.Background(), llm.Request{})
	assertCode(t, err, "credential_revoked")
	if operationErr := restarted.SeedConnections(context.Background(), []Connection{{Provider: "deepseek", Endpoint: view.Connections[0].Endpoint, APIKey: "secret-key"}}); operationErr != nil { // Synthetic test credential. gitleaks:allow
		t.Fatal(operationErr)
	}
	if restarted.Snapshot().Health != "suspended" {
		t.Fatal("startup seeding revived a revoked key")
	}
}
func TestProbeIdempotencyExpiredKeysAndRestartMarkers(t *testing.T) {
	repo := &memoryRepo{}
	var calls atomic.Int32
	factory := func(p models.Profile, c Connection) (llm.Provider, error) {
		inner, _ := echoFactory(p, c)
		return providerFunc(func(ctx context.Context, r llm.Request) (llm.Response, error) {
			calls.Add(1)
			return inner.Complete(ctx, r)
		}), nil
	}
	m := newTestManager(t, repo, factory)
	stage := stageDraft(t, m, 0, "secret-key")
	request := ProbeRequest{ExpectedRevision: 0, StageID: stage.ID, ProfileID: "ds-flash", Kind: "text", IdempotencyKey: probeKey()}
	first, err := m.Probe(context.Background(), "admin", request)
	if err != nil {
		t.Fatal(err)
	}
	waitProbe(t, m, first.ID)
	again, err := m.Probe(context.Background(), "admin", request)
	if err != nil || again.ID != first.ID || calls.Load() != 1 {
		t.Fatalf("duplicate paid probe: %v calls=%d", err, calls.Load())
	}
	request.Kind = "tools"
	_, err = m.Probe(context.Background(), "admin", request)
	assertCode(t, err, "idempotency_conflict")
	request.IdempotencyKey = fmt.Sprintf("%d:%s", time.Now().Add(-StageTTL-time.Second).UnixMilli(), randomID())
	_, err = m.Probe(context.Background(), "admin", request)
	assertCode(t, err, "idempotency_key_expired")
	m.mu.Lock()
	candidate := cloneState(m.state)
	r := candidate.Probes[first.ID]
	r.View.Status = "running"
	candidate.Probes[first.ID] = r
	if operationErr := m.persistLocked(context.Background(), candidate, "test"); operationErr != nil {
		t.Fatal(operationErr)
	}
	m.mu.Unlock()
	restarted := newTestManager(t, repo, factory)
	view, err := restarted.ProbeStatus(context.Background(), "admin", first.ID)
	if err != nil || view.Status != "interrupted_unknown" {
		t.Fatalf("restart marker: %+v %v", view, err)
	}
	request.Kind = "text"
	request.IdempotencyKey = r.Key
	view, err = restarted.Probe(context.Background(), "admin", request)
	if err != nil || view.ID != first.ID || calls.Load() != 1 {
		t.Fatal("restart reran a charged probe")
	}
}
func TestVisionOracleAndEligibilityRequireActualImages(t *testing.T) {
	m := newTestManager(t, &memoryRepo{}, echoFactory)
	settings := draftSettings()
	settings.Policy.Vision = "ds-flash"
	stage, err := m.Stage(context.Background(), "admin", 0, settings, []ConnectionMutation{{Provider: "deepseek", APIKeyAction: "replace", APIKey: "secret-key"}}) // Synthetic test credential. gitleaks:allow
	if err != nil {
		t.Fatal(err)
	}
	checkStage(t, m, stage, "text")
	checkStage(t, m, stage, "tools")
	if _, activationErr := m.Activate(context.Background(), "admin", 0, stage.ID); activationErr == nil {
		t.Fatal("vision activated without an image check")
	}
	checkStage(t, m, stage, "vision")
	view, err := m.Activate(context.Background(), "admin", 0, stage.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, bindings, _, _ := m.Bindings()
	if !bindings["ds-flash"].Images || view.Profiles[0].Eligibility != "production_eligible" {
		t.Fatal("verified image capability missing")
	}
}
func TestTextOnlyVisionCheckMakesNoCallAndThinkingCannotPromote(t *testing.T) {
	var calls atomic.Int32
	factory := func(p models.Profile, c Connection) (llm.Provider, error) {
		inner, _ := echoFactory(p, c)
		return providerFunc(func(ctx context.Context, r llm.Request) (llm.Response, error) {
			calls.Add(1)
			return inner.Complete(ctx, r)
		}), nil
	}
	m := newTestManager(t, &memoryRepo{}, factory)
	settings := draftSettings()
	settings.Profiles[0].Model = "deepseek-v4-pro"
	settings.Profiles[0].Thinking = "enabled"
	settings.Profiles[0].ReasoningEffort = "high"
	stage, err := m.Stage(context.Background(), "admin", 0, settings, []ConnectionMutation{{Provider: "deepseek", APIKeyAction: "replace", APIKey: "secret-key"}}) // Synthetic test credential. gitleaks:allow
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Probe(context.Background(), "admin", ProbeRequest{StageID: stage.ID, ProfileID: "ds-flash", Kind: "vision", IdempotencyKey: probeKey()})
	assertCode(t, err, "unsupported_images")
	if calls.Load() != 0 {
		t.Fatal("text-only vision reached the API")
	}
	checkStage(t, m, stage, "text")
	checkStage(t, m, stage, "tools")
	_, err = m.Activate(context.Background(), "admin", 0, stage.ID)
	assertCode(t, err, "verification_required")
}
func TestActivationPublisherSerializedAndFailureVisible(t *testing.T) {
	m := newTestManager(t, &memoryRepo{}, echoFactory)
	if operationErr := m.SetPublisher(func(*llm.ProfileSnapshot) error { return errors.New("publication failed") }); operationErr != nil {
		t.Fatal(operationErr)
	}
	stage := stageDraft(t, m, 0, "secret-key")
	checkStage(t, m, stage, "text")
	checkStage(t, m, stage, "tools")
	view, err := m.Activate(context.Background(), "admin", 0, stage.ID)
	assertCode(t, err, "activation_failed")
	if view.Revision != 1 || view.ActiveRevision != 0 || view.ActivationStatus != "failed" {
		t.Fatalf("desired/active failure hidden: %+v", view)
	}
	if operationErr := m.SetPublisher(func(*llm.ProfileSnapshot) error { return nil }); operationErr != nil {
		t.Fatal(operationErr)
	}
	if m.Snapshot().ActiveRevision != 1 || m.Snapshot().ActivationStatus != "applied" {
		t.Fatal("publication recovery did not report active revision")
	}
}
func TestConcurrentAdmissionRevokeLinearization(t *testing.T) {
	m := newTestManager(t, &memoryRepo{}, echoFactory)
	view := activateDraft(t, m, stageDraft(t, m, 0, "secret-key"))
	generation := view.Connections[0].Generation
	var wg sync.WaitGroup
	start := make(chan struct{})
	var rejected atomic.Int32
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, release, err := m.admit(context.Background(), "deepseek", generation, "")
			if err != nil {
				rejected.Add(1)
				return
			}
			release()
		}()
	}
	close(start)
	if _, operationErr := m.Revoke(context.Background(), "admin", 1, "deepseek", generation); operationErr != nil {
		t.Fatal(operationErr)
	}
	wg.Wait()
	for i := 0; i < 32; i++ {
		_, _, err := m.admit(context.Background(), "deepseek", generation, "")
		assertCode(t, err, "credential_revoked")
	}
}

func TestEnvironmentOwnershipAndSecretDenyCannotBeBypassed(t *testing.T) {
	m := newTestManager(t, &memoryRepo{}, echoFactory)
	if operationErr := m.SeedConnections(context.Background(), []Connection{{Provider: "deepseek", APIKey: "environment-key", Source: "environment"}}); operationErr != nil { // Synthetic test credential. gitleaks:allow
		t.Fatal(operationErr)
	}
	_, err := m.Stage(context.Background(), "admin", 0, draftSettings(), []ConnectionMutation{{Provider: "deepseek", APIKeyAction: "replace", APIKey: "replacement-key"}}) // Synthetic test credential. gitleaks:allow
	var coded *Error
	if !errors.As(err, &coded) || len(coded.FieldErrors) == 0 || coded.FieldErrors[0].Code != "environment_override" {
		t.Fatalf("env override accepted: %v", err)
	}
	if seedErr := m.SeedConnections(context.Background(), []Connection{{Provider: "deepseek", APIKey: "different-env-key", Source: "environment"}}); seedErr == nil { // Synthetic test credential. gitleaks:allow
		t.Fatal("startup mismatch silently changed identity")
	}
	// Clearing the actual environment source keeps the generation stable but
	// permits an explicitly staged replacement.
	if operationErr := m.SeedConnections(context.Background(), []Connection{{Provider: "deepseek", APIKey: "environment-key", Source: "TOML"}}); operationErr != nil { // Synthetic test credential. gitleaks:allow
		t.Fatal(operationErr)
	}
	stage := stageDraft(t, m, 0, "replacement-key")
	view := activateDraft(t, m, stage)
	if _, operationErr := m.Revoke(context.Background(), "admin", view.Revision, "deepseek", view.Connections[0].Generation); operationErr != nil {
		t.Fatal(operationErr)
	}
	_, err = m.Stage(context.Background(), "admin", 2, draftSettings(), []ConnectionMutation{{Provider: "deepseek", APIKeyAction: "replace", APIKey: "replacement-key"}}) // Synthetic test credential. gitleaks:allow
	if !errors.As(err, &coded) || len(coded.FieldErrors) == 0 || coded.FieldErrors[0].Code != "credential_revoked" {
		t.Fatalf("old secret gained a fresh allowed generation: %v", err)
	}
	// Retired environment-key was also denied when rotation committed.
	_, err = m.Stage(context.Background(), "admin", 2, draftSettings(), []ConnectionMutation{{Provider: "deepseek", APIKeyAction: "replace", APIKey: "environment-key"}}) // Synthetic test credential. gitleaks:allow
	if !errors.As(err, &coded) || len(coded.FieldErrors) == 0 || coded.FieldErrors[0].Code != "credential_revoked" {
		t.Fatalf("retired secret regained admission: %v", err)
	}
}
func TestMissingOptionalPresetDoesNotBlockAndHintsRequireEvidence(t *testing.T) {
	m := newTestManager(t, &memoryRepo{}, echoFactory)
	settings := draftSettings()
	settings.Profiles = append(settings.Profiles, models.CandidateProfiles()[3])
	stage, err := m.Stage(context.Background(), "admin", 0, settings, []ConnectionMutation{{Provider: "deepseek", APIKeyAction: "replace", APIKey: "secret-key"}}) // Synthetic test credential. gitleaks:allow
	if err != nil {
		t.Fatal(err)
	}
	activateDraft(t, m, stage)
	_, bindings, _, _ := m.Bindings()
	if bindings["glm-main"].Provider != nil || bindings["glm-main"].Eligibility != "experimental" {
		t.Fatal("unconfigured preset was admitted")
	}
	settings.Policy.LegacyProfileHints = map[string]string{"creative-custom": "glm-main"}
	_, err = m.Stage(context.Background(), "admin", 1, settings, nil)
	if err == nil {
		t.Fatal("unconfigured hint became an active target")
	}
}
func TestProbeConcurrencyCancellationAndWrongIdentity(t *testing.T) {
	var calls atomic.Int32
	block := make(chan struct{})
	entered := make(chan struct{}, 2)
	factory := func(p models.Profile, c Connection) (llm.Provider, error) {
		return providerFunc(func(ctx context.Context, r llm.Request) (llm.Response, error) {
			calls.Add(1)
			entered <- struct{}{}
			select {
			case <-ctx.Done():
				return llm.Response{}, ctx.Err()
			case <-block:
				return llm.Response{Content: `{"nonce":"wrong"}`, Model: p.Model, ModelVerified: false, FinishReason: "stop"}, nil
			}
		}), nil
	}
	m := newTestManager(t, &memoryRepo{}, factory)
	stage := stageDraft(t, m, 0, "secret-key")
	first, err := m.Probe(context.Background(), "admin", ProbeRequest{StageID: stage.ID, ProfileID: "ds-flash", Kind: "text", IdempotencyKey: probeKey()})
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	_, err = m.Probe(context.Background(), "admin", ProbeRequest{StageID: stage.ID, ProfileID: "ds-flash", Kind: "tools", IdempotencyKey: probeKey()})
	assertCode(t, err, "probe_busy")
	canceled, err := m.CancelProbe(context.Background(), "admin", first.ID)
	if err != nil || canceled.Status != "cancel_requested" {
		t.Fatalf("cancel: %+v %v", canceled, err)
	}
	finished := waitProbe(t, m, first.ID)
	if finished.Status != "cancelled" || finished.Result.Passed { //nolint:misspell // Preserve the persisted API status spelling.
		t.Fatal("canceled check promoted a model")
	}
	next, err := m.Probe(context.Background(), "admin", ProbeRequest{StageID: stage.ID, ProfileID: "ds-flash", Kind: "text", IdempotencyKey: probeKey()})
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	close(block)
	finished = waitProbe(t, m, next.ID)
	if finished.Status != "failed" || finished.Result.ErrorCode != "unverified_model_identity" {
		t.Fatalf("unknown wire model passed: %+v", finished)
	}
	if calls.Load() != 2 {
		t.Fatalf("busy or cancel reran the provider: %d", calls.Load())
	}
}
func TestLateResultAfterDiscardNeverPublishesEvidence(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	factory := func(p models.Profile, c Connection) (llm.Provider, error) {
		normal, _ := echoFactory(p, c)
		return providerFunc(func(ctx context.Context, r llm.Request) (llm.Response, error) {
			entered <- struct{}{}
			<-release
			return normal.Complete(context.Background(), r)
		}), nil
	}
	m := newTestManager(t, &memoryRepo{}, factory)
	stage := stageDraft(t, m, 0, "secret-key")
	view, err := m.Probe(context.Background(), "admin", ProbeRequest{StageID: stage.ID, ProfileID: "ds-flash", Kind: "text", IdempotencyKey: probeKey()})
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	if operationErr := m.Discard(context.Background(), "admin", 0, stage.ID); operationErr != nil {
		t.Fatal(operationErr)
	}
	close(release)
	finished := waitProbe(t, m, view.ID)
	if finished.Result.Passed || finished.Status == "completed" {
		t.Fatal("discarded candidate gained late eligibility")
	}
}
func TestFailedEvidencePersistenceNeverReturnsPassedBadge(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	repo := &memoryRepo{}
	factory := func(p models.Profile, c Connection) (llm.Provider, error) {
		normal, _ := echoFactory(p, c)
		return providerFunc(func(ctx context.Context, r llm.Request) (llm.Response, error) {
			entered <- struct{}{}
			<-release
			return normal.Complete(ctx, r)
		}), nil
	}
	m := newTestManager(t, repo, factory)
	stage := stageDraft(t, m, 0, "secret-key")
	view, err := m.Probe(context.Background(), "admin", ProbeRequest{StageID: stage.ID, ProfileID: "ds-flash", Kind: "text", IdempotencyKey: probeKey()})
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	repo.setFailure(true)
	close(release)
	finished := waitProbe(t, m, view.ID)
	if finished.Status != "interrupted_unknown" || finished.Result != nil {
		t.Fatalf("uncommitted evidence returned a passed badge: %+v", finished)
	}
	repo.setFailure(false)
	restarted := newTestManager(t, repo, echoFactory)
	finished, err = restarted.ProbeStatus(context.Background(), "admin", view.ID)
	if err != nil || finished.Status != "interrupted_unknown" {
		t.Fatal("restart lost uncertain charge marker")
	}
}
func TestModelIdentityDoesNotConfuseGLMMainWithFlash(t *testing.T) {
	main := models.CandidateProfiles()[3]
	if modelIdentityMatches(main, "glm-5.3-flash") || modelIdentityMatches(main, "glm-5.3-flash-20261001") {
		t.Fatal("Flash identity was accepted as flagship")
	}
	if !modelIdentityMatches(main, "glm-5.3-20261001") {
		t.Fatal("flagship served version was rejected")
	}
}

func TestLegacyAdmissionAndSafeSeedRefresh(t *testing.T) {
	repo := &memoryRepo{}
	m := newTestManager(t, repo, echoFactory)
	if operationErr := m.SeedConnections(context.Background(), []Connection{{Provider: "deepseek", APIKey: "old-key", Source: "TOML"}}); operationErr != nil {
		t.Fatal(operationErr)
	}
	called := atomic.Int32{}
	inner := providerFunc(func(context.Context, llm.Request) (llm.Response, error) {
		called.Add(1)
		return llm.Response{Content: "ok"}, nil
	})
	old, err := m.BindLegacy("deepseek", inner)
	if err != nil {
		t.Fatal(err)
	}
	if _, operationErr := old.Complete(context.Background(), llm.Request{}); operationErr != nil {
		t.Fatal(operationErr)
	}
	oldGen := m.Snapshot().Connections[0].Generation
	if operationErr := m.SeedConnections(context.Background(), []Connection{{Provider: "deepseek", APIKey: "fresh-key", Source: "TOML"}}); operationErr != nil { // Synthetic test credential. gitleaks:allow
		t.Fatal(operationErr)
	}
	if m.Snapshot().Connections[0].Generation == oldGen || m.Snapshot().Settings.Policy.Everyday != "" {
		t.Fatal("legacy seed refresh activated a managed policy or retained old identity")
	}
	_, err = old.Complete(context.Background(), llm.Request{})
	assertCode(t, err, "credential_revoked")
	restarted := newTestManager(t, repo, echoFactory)
	guarded, err := restarted.BindLegacy("deepseek", inner)
	if err != nil {
		t.Fatal(err)
	}
	view := restarted.Snapshot()
	if _, operationErr := restarted.Revoke(context.Background(), "admin", 0, "deepseek", view.Connections[0].Generation); operationErr != nil {
		t.Fatal(operationErr)
	}
	_, err = guarded.Complete(context.Background(), llm.Request{})
	assertCode(t, err, "credential_revoked")
	if called.Load() != 1 {
		t.Fatal("legacy admission ignored deny")
	}
}
func TestProbeRateWindowSurvivesRestartAndPasswordGate(t *testing.T) {
	repo := &memoryRepo{}
	m := newTestManager(t, repo, echoFactory)
	stage := stageDraft(t, m, 0, "secret-key")
	for i := 0; i < 5; i++ {
		checkStage(t, m, stage, "text")
	}
	restarted := newTestManager(t, repo, echoFactory)
	_, err := restarted.Probe(context.Background(), "admin", ProbeRequest{StageID: stage.ID, ProfileID: "ds-flash", Kind: "text", IdempotencyKey: probeKey()})
	assertCode(t, err, "probe_rate_limit")
	restarted.authorize = func(context.Context, string) error { return &Error{Code: "password_change_required", HTTPStatus: 403} }
	_, err = restarted.Stage(context.Background(), "admin", 0, draftSettings(), nil)
	assertCode(t, err, "password_change_required")
}
func TestProbeRejectsUnfinishedCompletionAndNoSecretsInMarkers(t *testing.T) {
	factory := func(p models.Profile, c Connection) (llm.Provider, error) {
		normal, _ := echoFactory(p, c)
		return providerFunc(func(ctx context.Context, r llm.Request) (llm.Response, error) {
			res, err := normal.Complete(ctx, r)
			res.FinishReason = ""
			return res, err
		}), nil
	}
	m := newTestManager(t, &memoryRepo{}, factory)
	stage := stageDraft(t, m, 0, "very-private-api-key")
	view, err := m.Probe(context.Background(), "admin", ProbeRequest{StageID: stage.ID, ProfileID: "ds-flash", Kind: "text", IdempotencyKey: probeKey()})
	if err != nil {
		t.Fatal(err)
	}
	view = waitProbe(t, m, view.ID)
	if view.Result.Passed || view.Result.ErrorCode != "incomplete_probe_response" {
		t.Fatal("unfinished response produced success evidence")
	}
	safe, _ := json.Marshal(view)
	if strings.Contains(string(safe), "very-private-api-key") || strings.Contains(string(safe), "opaque test reasoning") {
		t.Fatal("secret or reasoning escaped probe summary")
	}
}

func TestStageProbeRestageAssignmentsPreservesPendingGenerationAndEvidence(t *testing.T) {
	m := newTestManager(t, &memoryRepo{}, echoFactory)
	settings := draftSettings()
	settings.Policy.Everyday = ""
	first, err := m.Stage(context.Background(), "admin", 0, settings, []ConnectionMutation{{Provider: "deepseek", APIKeyAction: "replace", APIKey: "pending-key"}}) // Synthetic test credential. gitleaks:allow
	if err != nil {
		t.Fatal(err)
	}
	checkStage(t, m, first, "text")
	checkStage(t, m, first, "tools")
	generation := first.Connections[0].Generation
	settings.Policy.Everyday = "ds-flash"
	settings.Profiles[0].Name = "A clearer display name"
	second, err := m.Stage(context.Background(), "admin", 0, settings, []ConnectionMutation{{Provider: "deepseek", APIKeyAction: "keep"}})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID || second.Connections[0].Generation != generation {
		t.Fatal("restage lost pending identity or retained obsolete stage identity")
	}
	if second.Profiles[0].Eligibility != "production_eligible" || len(second.Profiles[0].Evidence) != 2 {
		t.Fatal("name/role restage lost unchanged protocol evidence")
	}
	view, err := m.Activate(context.Background(), "admin", 0, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Settings.Policy.Everyday != "ds-flash" || view.Connections[0].Generation != generation {
		t.Fatal("restage activation used a different credential")
	}
}
func TestSameActorRestageBusyDuringProbeAndChangeInvalidatesEvidence(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	factory := func(p models.Profile, c Connection) (llm.Provider, error) {
		normal, _ := echoFactory(p, c)
		return providerFunc(func(ctx context.Context, r llm.Request) (llm.Response, error) {
			entered <- struct{}{}
			select {
			case <-release:
				return normal.Complete(ctx, r)
			case <-ctx.Done():
				return llm.Response{}, ctx.Err()
			}
		}), nil
	}
	m := newTestManager(t, &memoryRepo{}, factory)
	first := stageDraft(t, m, 0, "pending-key")
	probe, err := m.Probe(context.Background(), "admin", ProbeRequest{StageID: first.ID, ProfileID: "ds-flash", Kind: "text", IdempotencyKey: probeKey()})
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	_, err = m.Stage(context.Background(), "admin", 0, draftSettings(), nil)
	assertCode(t, err, "stage_busy")
	close(release)
	waitProbe(t, m, probe.ID)
	settings := draftSettings()
	settings.Profiles[0].MaxOutputTokens = 4096
	second, err := m.Stage(context.Background(), "admin", 0, settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.Profiles[0].Eligibility != "experimental" || len(second.Profiles[0].Evidence) != 0 {
		t.Fatal("protocol parameter edit reused old evidence")
	}
}
func TestRevokedAssignedProfileStartsWithSuspendedBindingsEvenFactoryRefusesDisabled(t *testing.T) {
	repo := &memoryRepo{}
	factory := func(p models.Profile, c Connection) (llm.Provider, error) {
		if !c.Enabled {
			return nil, errors.New("disabled")
		}
		return echoFactory(p, c)
	}
	m := newTestManager(t, repo, factory)
	view := activateDraft(t, m, stageDraft(t, m, 0, "secret-key"))
	if _, operationErr := m.Revoke(context.Background(), "admin", 1, "deepseek", view.Connections[0].Generation); operationErr != nil {
		t.Fatal(operationErr)
	}
	restarted := newTestManager(t, repo, factory)
	if operationErr := restarted.SetPublisher(func(*llm.ProfileSnapshot) error { return nil }); operationErr != nil {
		t.Fatal(operationErr)
	}
	_, bindings, _, err := restarted.Bindings()
	if err != nil {
		t.Fatal(err)
	}
	_, err = bindings["ds-flash"].Provider.Complete(context.Background(), llm.Request{})
	assertCode(t, err, "credential_revoked")
	if restarted.Snapshot().Health != "suspended" {
		t.Fatal("recovery startup hid the suspended role")
	}
}

func TestClosePreservesUnknownMarkersAndStopsAdmissions(t *testing.T) {
	entered := make(chan struct{}, 1)
	factory := func(p models.Profile, c Connection) (llm.Provider, error) {
		return providerFunc(func(ctx context.Context, r llm.Request) (llm.Response, error) {
			entered <- struct{}{}
			<-ctx.Done()
			return llm.Response{}, ctx.Err()
		}), nil
	}
	repo := &memoryRepo{}
	m := newTestManager(t, repo, factory)
	stage := stageDraft(t, m, 0, "secret-key")
	view, err := m.Probe(context.Background(), "admin", ProbeRequest{StageID: stage.ID, ProfileID: "ds-flash", Kind: "text", IdempotencyKey: probeKey()})
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	if operationErr := m.Close(context.Background()); operationErr != nil {
		t.Fatal(operationErr)
	}
	finished, err := m.ProbeStatus(context.Background(), "admin", view.ID)
	if err != nil || finished.Status != "interrupted_unknown" {
		t.Fatal("shutdown lost the uncertain probe marker")
	}
	_, _, err = m.admit(context.Background(), "deepseek", stage.Connections[0].Generation, stage.ID)
	assertCode(t, err, "runtime_stopped")
	restarted := newTestManager(t, repo, echoFactory)
	finished, err = restarted.ProbeStatus(context.Background(), "admin", view.ID)
	if err != nil || finished.Status != "interrupted_unknown" {
		t.Fatal("restart reran or lost a shutdown probe")
	}
}
func TestExpiredStageCannotPublishOrActivate(t *testing.T) {
	m := newTestManager(t, &memoryRepo{}, echoFactory)
	stage := stageDraft(t, m, 0, "secret-key")
	m.mu.Lock()
	m.state.Stage.ExpiresAt = time.Now().Add(-time.Second)
	m.mu.Unlock()
	_, err := m.Activate(context.Background(), "admin", 0, stage.ID)
	assertCode(t, err, "stage_expired")
	_, err = m.Probe(context.Background(), "admin", ProbeRequest{StageID: stage.ID, ProfileID: "ds-flash", Kind: "text", IdempotencyKey: probeKey()})
	assertCode(t, err, "stage_expired")
	if m.Snapshot().Stage != nil {
		t.Fatal("expired stage was presented as current")
	}
}
func TestFailedRotationCommitRetainsOldKeyAndVerifiedStage(t *testing.T) {
	repo := &memoryRepo{}
	m := newTestManager(t, repo, echoFactory)
	view := activateDraft(t, m, stageDraft(t, m, 0, "old-key"))
	oldGen := view.Connections[0].Generation
	_, bindings, _, _ := m.Bindings()
	old := bindings["ds-flash"].Provider
	next := stageDraft(t, m, 1, "new-key")
	checkStage(t, m, next, "text")
	checkStage(t, m, next, "tools")
	repo.setFailure(true)
	_, err := m.Activate(context.Background(), "admin", 1, next.ID)
	assertCode(t, err, "storage_unavailable")
	if m.Snapshot().Revision != 1 || m.Snapshot().Connections[0].Generation != oldGen || m.Snapshot().Stage == nil {
		t.Fatal("failed transaction changed active credentials or discarded the tested candidate")
	}
	if _, operationErr := old.Complete(context.Background(), llm.Request{Message: "nonce deadbeef0123"}); operationErr != nil {
		t.Fatal("old credential stopped before a successful commit")
	}
}

func TestClaudeVisionCanBeVerifiedThroughTrustedLegacyConnection(t *testing.T) {
	m := newTestManager(t, &memoryRepo{}, echoFactory)
	if operationErr := m.SeedConnections(context.Background(), []Connection{{Provider: "claude", Endpoint: "https://api.anthropic.com", APIKey: "claude-key", Source: "TOML"}}); operationErr != nil { // Synthetic test credential. gitleaks:allow
		t.Fatal(operationErr)
	}
	profile := models.Profile{ID: "claude-old", Name: "Existing Claude", Connection: "claude", Model: "claude-sonnet-4-6", MaxOutputTokens: 4096, Thinking: "disabled"}
	settings := models.Settings{SchemaVersion: 1, Profiles: []models.Profile{profile}, Policy: models.Policy{Everyday: profile.ID, Vision: profile.ID}}
	stage, err := m.Stage(context.Background(), "admin", 0, settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"text", "tools", "vision"} {
		view, err := m.Probe(context.Background(), "admin", ProbeRequest{StageID: stage.ID, ProfileID: profile.ID, Kind: kind, IdempotencyKey: probeKey()})
		if err != nil {
			t.Fatal(err)
		}
		view = waitProbe(t, m, view.ID)
		if view.Result == nil || !view.Result.Passed {
			t.Fatalf("Claude %s check failed: %+v", kind, view)
		}
	}
	if _, operationErr := m.Activate(context.Background(), "admin", 0, stage.ID); operationErr != nil {
		t.Fatal(operationErr)
	}
	_, bindings, _, _ := m.Bindings()
	if !bindings[profile.ID].Images {
		t.Fatal("preserved Claude vision lacks proven image capability")
	}
}
func TestPolicyHistoryBoundedAndRestoreIsOnlyAStage(t *testing.T) {
	repo := &memoryRepo{}
	m := newTestManager(t, repo, echoFactory)
	view := activateDraft(t, m, stageDraft(t, m, 0, "secret-key"))
	generation := view.Connections[0].Generation
	for rev := 2; rev <= 5; rev++ {
		settings := cloneSettings(m.Snapshot().Settings)
		settings.Profiles[0].Name = fmt.Sprintf("Version %d", rev)
		stage, err := m.Stage(context.Background(), "admin", uint64(rev-1), settings, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, operationErr := m.Activate(context.Background(), "admin", uint64(rev-1), stage.ID); operationErr != nil {
			t.Fatal(operationErr)
		}
	}
	history, err := m.History(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 || history[0].Revision != 2 || history[2].Revision != 4 {
		t.Fatalf("history retention: %+v", history)
	}
	safe, _ := json.Marshal(history)
	if strings.Contains(string(safe), "secret-key") || strings.Contains(string(safe), "generation") {
		t.Fatal("policy history contains credential state")
	}
	history[0].Settings.Profiles[0].Model = "mutated"
	again, _ := m.History(context.Background(), "admin")
	if again[0].Settings.Profiles[0].Model == "mutated" {
		t.Fatal("caller mutated persisted history")
	}
	stage, err := m.StageHistory(context.Background(), "admin", 5, 2)
	if err != nil {
		t.Fatal(err)
	}
	if m.Snapshot().Revision != 5 || m.Snapshot().Settings.Profiles[0].Name != "Version 5" {
		t.Fatal("preparing history restored an active policy")
	}
	if stage.Settings.Profiles[0].Name != "Version 2" || stage.Connections[0].Generation != generation {
		t.Fatal("restore did not use the requested settings and current credential")
	}
	if _, restoreErr := m.StageHistory(context.Background(), "admin", 5, 2); restoreErr == nil {
		t.Fatal("history restore replaced a pending draft")
	}
	if _, operationErr := m.Activate(context.Background(), "admin", 5, stage.ID); operationErr != nil {
		t.Fatal(operationErr)
	}
	restarted := newTestManager(t, repo, echoFactory)
	retained, err := restarted.History(context.Background(), "admin")
	if err != nil || len(retained) != 3 {
		t.Fatal("history was lost at restart")
	}
	_, err = restarted.StageHistory(context.Background(), "admin", 6, 1)
	assertCode(t, err, "history_not_found")
	_, err = restarted.History(context.Background(), "ordinary")
	assertCode(t, err, "admin_required")
}
func TestHistoryRestoreCannotReviveCredentialsOrRemoveProviderBan(t *testing.T) {
	m := newTestManager(t, &memoryRepo{}, echoFactory)
	old := activateDraft(t, m, stageDraft(t, m, 0, "old-key"))
	oldGen := old.Connections[0].Generation
	rotated := activateDraft(t, m, stageDraft(t, m, 1, "new-key"))
	newGen := rotated.Connections[0].Generation
	stage, err := m.StageHistory(context.Background(), "admin", 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if stage.Connections[0].Generation != newGen || stage.Connections[0].Generation == oldGen {
		t.Fatal("history restored a previous credential generation")
	}
	if operationErr := m.Discard(context.Background(), "admin", 2, stage.ID); operationErr != nil {
		t.Fatal(operationErr)
	}
	if _, operationErr := m.Revoke(context.Background(), "admin", 2, "deepseek", newGen); operationErr != nil {
		t.Fatal(operationErr)
	}
	_, err = m.StageHistory(context.Background(), "admin", 3, 1)
	if err == nil {
		t.Fatal("history restore revived a revoked key")
	}
	// A current ban is copied into the restore instead of disappearing with the
	// historical policy; validation then rejects the old forbidden assignment.
	m.mu.Lock()
	m.state.Settings.Policy.ForbiddenProviders = []string{"deepseek"}
	m.mu.Unlock()
	_, err = m.StageHistory(context.Background(), "admin", 3, 1)
	var coded *Error
	if !errors.As(err, &coded) {
		t.Fatal(err)
	}
	found := false
	for _, field := range coded.FieldErrors {
		if field.Code == "forbidden_provider" {
			found = true
		}
	}
	if !found {
		t.Fatalf("history dropped current provider ban: %v", err)
	}
}
func TestEnvironmentOwnershipSyncPreservesKeyAndAllowsRemovalRecovery(t *testing.T) {
	repo := &memoryRepo{}
	m := newTestManager(t, repo, echoFactory)
	if operationErr := m.SeedConnections(context.Background(), []Connection{{Provider: "deepseek", APIKey: "env-key", Source: "env"}}); operationErr != nil {
		t.Fatal(operationErr)
	}
	stage, err := m.Stage(context.Background(), "admin", 0, draftSettings(), nil)
	if err != nil {
		t.Fatal(err)
	}
	generation := stage.Connections[0].Generation
	// Simulate an older persisted source label on both active and staged handles.
	m.mu.Lock()
	c := m.state.Connections["deepseek"]
	c.Connection.Source = "env"
	m.state.Connections["deepseek"] = c
	c = m.state.Stage.Connections["deepseek"]
	c.Connection.Source = "env"
	m.state.Stage.Connections["deepseek"] = c
	if operationErr := m.persistLocked(context.Background(), m.state, "test"); operationErr != nil {
		t.Fatal(operationErr)
	}
	m.mu.Unlock()
	restarted := newTestManager(t, repo, echoFactory)
	if restarted.Snapshot().Connections[0].Source != "environment" || restarted.Snapshot().Stage.Connections[0].Source != "environment" {
		t.Fatal("persisted env alias was not normalized")
	}
	if operationErr := restarted.SyncEnvironmentOwnership(context.Background(), map[string]bool{"deepseek": false}); operationErr != nil {
		t.Fatal(operationErr)
	}
	view := restarted.Snapshot()
	if view.Connections[0].Source != "encrypted_store" || view.Connections[0].Generation != generation || view.Stage.Connections[0].Generation != generation {
		t.Fatal("removing env ownership changed the credential generation")
	}
	if operationErr := restarted.Discard(context.Background(), "admin", 0, view.Stage.ID); operationErr != nil {
		t.Fatal(operationErr)
	}
	if _, operationErr := restarted.Stage(context.Background(), "admin", 0, draftSettings(), []ConnectionMutation{{Provider: "deepseek", APIKeyAction: "replace", APIKey: "fresh-key"}}); operationErr != nil { // Synthetic test credential. gitleaks:allow
		t.Fatal("removed environment override still blocked rotation: ", operationErr)
	}
}

func TestWaitAfterCloseIncludesAdmissionsAndProbeFinalization(t *testing.T) {
	entered := make(chan struct{}, 2)
	released := make(chan struct{})
	factory := func(p models.Profile, c Connection) (llm.Provider, error) {
		return providerFunc(func(ctx context.Context, r llm.Request) (llm.Response, error) {
			entered <- struct{}{}
			<-ctx.Done()
			<-released // represents synchronous observer/storage finalization
			return llm.Response{}, ctx.Err()
		}), nil
	}
	m := newTestManager(t, &memoryRepo{}, factory)
	if err := m.Wait(context.Background()); err == nil {
		t.Fatal("wait accepted before close")
	}
	stage := stageDraft(t, m, 0, "shutdown-probe-key")
	if _, operationErr := m.Probe(context.Background(), "admin", ProbeRequest{StageID: stage.ID, ProfileID: "ds-flash", Kind: "text", IdempotencyKey: probeKey()}); operationErr != nil {
		t.Fatal(operationErr)
	}
	<-entered
	if operationErr := m.Close(context.Background()); operationErr != nil {
		t.Fatal(operationErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := m.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait did not wait for finalizer: %v", err)
	}
	close(released)
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if operationErr := m.Wait(ctx2); operationErr != nil {
		t.Fatal(operationErr)
	}
	m.mu.Lock()
	pending := len(m.admissions) + len(m.running)
	m.mu.Unlock()
	if pending != 0 {
		t.Fatal("probe or admission still active")
	}
}

func legacyDraft() models.Settings {
	return models.Settings{SchemaVersion: 1, Profiles: []models.Profile{
		{ID: "claude-existing", Name: "Existing Claude", Connection: "claude", Model: "claude-sonnet-4-6", MaxOutputTokens: 4096, Thinking: "disabled"},
		{ID: "openai-existing", Name: "Existing OpenAI", Connection: "openai", Model: "gpt-4o", MaxOutputTokens: 4096, Thinking: "disabled"},
		{ID: "ollama-existing", Name: "Existing Ollama", Connection: "ollama", Model: "qwen3:8b", MaxOutputTokens: 4096, Thinking: "disabled"},
	}, Policy: models.Policy{Everyday: "openai-existing", Vision: "claude-existing", LegacyProfileHints: map[string]string{"local": "ollama-existing"}}}
}

func TestExactLegacyPreservationAndRestartWithoutFabricatedProof(t *testing.T) {
	repo := &memoryRepo{}
	m := newTestManager(t, repo, echoFactory)
	if err := m.SeedConnections(context.Background(), []Connection{
		{Provider: "claude", Endpoint: "https://api.anthropic.com", APIKey: "claude-sealed", Source: "legacy_effective"}, // Synthetic test credential. gitleaks:allow
		{Provider: "openai", Endpoint: "https://api.openai.com/v1", APIKey: "openai-sealed", Source: "environment"},      // Synthetic test credential. gitleaks:allow
		{Provider: "ollama", Endpoint: "http://localhost:11434", Source: "legacy_effective"},
	}); err != nil {
		t.Fatal(err)
	}
	settings := legacyDraft()
	if operationErr := m.SeedLegacyProfiles(context.Background(), settings); operationErr != nil {
		t.Fatal(operationErr)
	}
	baseline, ok := m.LegacySettings()
	if !ok {
		t.Fatal("missing baseline")
	}
	baseline.Profiles[0].Model = "mutated"
	untouched, _ := m.LegacySettings()
	if untouched.Profiles[0].Model == "mutated" {
		t.Fatal("mutable baseline alias")
	}
	stage, err := m.Stage(context.Background(), "admin", 0, settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range stage.Profiles {
		if p.Eligibility != "legacy_preserved" || len(p.Evidence) != 0 {
			t.Fatal("legacy preservation fabricated evidence or lost exact grant")
		}
	}
	if _, operationErr := m.Activate(context.Background(), "admin", 0, stage.ID); operationErr != nil {
		t.Fatal(operationErr)
	}
	_, bindings, _, err := m.Bindings()
	if err != nil {
		t.Fatal(err)
	}
	if !bindings["claude-existing"].Images || bindings["openai-existing"].Images || bindings["ollama-existing"].Images {
		t.Fatal("incorrect legacy image capabilities")
	}
	restarted := newTestManager(t, repo, echoFactory)
	if operationErr := restarted.SyncEnvironmentOwnership(context.Background(), map[string]bool{}); operationErr != nil {
		t.Fatal(operationErr)
	}
	for _, p := range restarted.Snapshot().Profiles {
		if p.Eligibility != "legacy_preserved" {
			t.Fatal("restart or source metadata invalidated exact grant")
		}
	}
	handle, err := restarted.LegacyConnection("openai")
	if err != nil || handle.APIKey != "openai-sealed" {
		t.Fatal("bootstrap did not receive sealed key")
	}
	raw, _ := json.Marshal(handle)
	if strings.Contains(string(raw), "openai-sealed") {
		t.Fatal("key leaked through JSON")
	}
	settings.Profiles[0].Name = "Name only"
	next, err := restarted.Stage(context.Background(), "admin", 1, settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	if next.Profiles[0].Eligibility != "legacy_preserved" {
		t.Fatal("display rename lost identity")
	}
	if operationErr := restarted.Discard(context.Background(), "admin", 1, next.ID); operationErr != nil {
		t.Fatal(operationErr)
	}
	for _, change := range []func(*models.Settings){
		func(s *models.Settings) { s.Profiles[0].Model = "claude-sonnet-new" },
		func(s *models.Settings) { s.Profiles[0].MaxOutputTokens = 8192 },
		func(s *models.Settings) { s.Profiles[0].ID = "claude-new"; s.Policy.Vision = "claude-new" },
	} {
		modified := cloneSettings(settings)
		change(&modified)
		stage, err := restarted.Stage(context.Background(), "admin", 1, modified, nil)
		if err != nil {
			t.Fatal(err)
		}
		if stage.Profiles[0].Eligibility == "legacy_preserved" {
			t.Fatal("changed protocol inherited legacy grant")
		}
		if _, err := restarted.Activate(context.Background(), "admin", 1, stage.ID); err == nil {
			t.Fatal("changed protocol activated without evidence")
		}
		if operationErr := restarted.Discard(context.Background(), "admin", 1, stage.ID); operationErr != nil {
			t.Fatal(operationErr)
		}
	}
	conn, _ := restarted.LegacyConnection("claude")
	if _, operationErr := restarted.Revoke(context.Background(), "admin", 1, "claude", conn.Generation); operationErr != nil {
		t.Fatal(operationErr)
	}
	if operationErr := restarted.SeedLegacyProfiles(context.Background(), settings); operationErr != nil {
		t.Fatal(operationErr)
	}
	if restarted.Snapshot().Profiles[0].Eligibility != "suspended" {
		t.Fatal("seed resurrected revoked grant")
	}
}

func TestLegacyGrantCannotBeReseededOrAppliedToNewVendors(t *testing.T) {
	m := newTestManager(t, &memoryRepo{}, echoFactory)
	if operationErr := m.SeedConnections(context.Background(), []Connection{{Provider: "openai", Endpoint: "https://api.openai.com/v1", APIKey: "initial-key", Source: "legacy_effective"}, {Provider: "deepseek", APIKey: "ds-key", Source: "legacy_effective"}}); operationErr != nil { // Synthetic test credential. gitleaks:allow
		t.Fatal(operationErr)
	}
	settings := draftSettings()
	settings.Profiles = append(settings.Profiles, legacyDraft().Profiles[1])
	settings.Policy.Everyday = "openai-existing"
	if operationErr := m.SeedLegacyProfiles(context.Background(), settings); operationErr != nil {
		t.Fatal(operationErr)
	}
	if operationErr := m.SeedConnections(context.Background(), []Connection{{Provider: "openai", Endpoint: "https://api.openai.com/v1", APIKey: "rotated-key", Source: "legacy_effective"}}); operationErr != nil { // Synthetic test credential. gitleaks:allow
		t.Fatal(operationErr)
	}
	if operationErr := m.SeedLegacyProfiles(context.Background(), settings); operationErr != nil {
		t.Fatal(operationErr)
	}
	stage, err := m.Stage(context.Background(), "admin", 0, settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range stage.Profiles {
		if p.Eligibility == "legacy_preserved" {
			t.Fatal("new generation/vendor inherited grant")
		}
	}
}

func TestChangedLegacyThinkingCannotInheritPreservation(t *testing.T) {
	m := newTestManager(t, &memoryRepo{}, echoFactory)
	if err := m.SeedConnections(context.Background(), []Connection{
		{Provider: "claude", APIKey: "old-thinking-key", Source: "legacy_effective"}, // Synthetic test credential. gitleaks:allow
		{Provider: "openai", APIKey: "old-openai-key", Source: "legacy_effective"},   // Synthetic test credential. gitleaks:allow
		{Provider: "ollama", Endpoint: "http://localhost:11434", Source: "legacy_effective"},
	}); err != nil {
		t.Fatal(err)
	}
	settings := legacyDraft()
	if operationErr := m.SeedLegacyProfiles(context.Background(), settings, "claude"); operationErr != nil {
		t.Fatal(operationErr)
	}
	stage, err := m.Stage(context.Background(), "admin", 0, settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range stage.Profiles {
		if p.ID == "claude-existing" && p.Eligibility == "legacy_preserved" {
			t.Fatal("changed thinking inherited preservation")
		}
		if p.ID != "claude-existing" && p.Eligibility != "legacy_preserved" {
			t.Fatal("unrelated legacy provider lost preservation")
		}
	}
	if _, activationErr := m.Activate(context.Background(), "admin", 0, stage.ID); activationErr == nil {
		t.Fatal("changed vision profile activated without checks")
	}
}

func TestCatalogRefreshPreservesEvidenceButProtocolChangeDoesNot(t *testing.T) {
	repo := &memoryRepo{}
	m := newTestManager(t, repo, echoFactory)
	stage := stageDraft(t, m, 0, "candidate-key")
	checkStage(t, m, stage, "text")
	checkStage(t, m, stage, "tools")
	m.mu.Lock()
	for kind, e := range m.state.Stage.Evidence["ds-flash"] {
		e.CatalogVersion = "old-documentation-date"
		m.state.Stage.Evidence["ds-flash"][kind] = e
	}
	profile := m.state.Stage.Settings.Profiles[0]
	connection := m.state.Stage.Connections[profile.Connection]
	if got := m.eligibilityLocked(profile, connection, m.state.Stage.Evidence[profile.ID]); got != "production_eligible" {
		t.Fatalf("unrelated catalog refresh invalidates working protocol: %s", got)
	}
	e := m.state.Stage.Evidence[profile.ID]["tools"]
	e.CompatibilityVersion = "obsolete-protocol"
	m.state.Stage.Evidence[profile.ID]["tools"] = e
	if got := m.eligibilityLocked(profile, connection, m.state.Stage.Evidence[profile.ID]); got != "experimental" {
		t.Fatalf("changed adapter contract inherited old evidence: %s", got)
	}
	m.mu.Unlock()
}

func TestVisionEvidenceVersionsSurviveRestartWithoutInvalidatingTextAndTools(t *testing.T) {
	for _, version := range []string{FixtureVersion, VisionFixtureVersion, "unrecognized-vision-fixture"} {
		t.Run(version, func(t *testing.T) {
			repo := &memoryRepo{}
			m := newTestManager(t, repo, echoFactory)
			stage := stageDraft(t, m, 0, "secret-key")
			checkStage(t, m, stage, "text")
			checkStage(t, m, stage, "tools")
			vision := checkStage(t, m, stage, "vision")
			if vision.Result.FixtureVersion != VisionFixtureVersion {
				t.Fatal("new vision evidence did not identify the enlarged raster")
			}
			if _, operationErr := m.Activate(context.Background(), "admin", 0, stage.ID); operationErr != nil {
				t.Fatal(operationErr)
			}
			m.mu.Lock()
			candidate := cloneState(m.state)
			evidence := candidate.Evidence["ds-flash"]["vision"]
			evidence.FixtureVersion = version
			candidate.Evidence["ds-flash"]["vision"] = evidence
			err := m.persistLocked(context.Background(), candidate, "test")
			m.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			restarted := newTestManager(t, repo, echoFactory)
			_, bindings, _, err := restarted.Bindings()
			if err != nil {
				t.Fatal(err)
			}
			binding := bindings["ds-flash"]
			if binding.Eligibility != "production_eligible" || binding.Images != (version != "unrecognized-vision-fixture") {
				t.Fatalf("retained proofs/unknown version admission: %+v", binding)
			}
		})
	}
}

func TestTypedStateClonePreservesEncodingAndIsolatesNestedMetadata(t *testing.T) {
	settings := draftSettings()
	settings.Policy.LegacyHints = map[string]string{"light": "background"}
	settings.Policy.Fallbacks = map[string][]string{"everyday": {"ds-flash"}}
	proof := Evidence{Kind: "text", Passed: true, Fingerprint: "original"}
	original := durableState{
		Settings:       settings,
		LegacyBaseline: cloneSettings(settings),
		Connections:    map[string]secretConnection{"deepseek": {Connection: Connection{Provider: "deepseek", APIKey: "fixture"}, Secret: "synthetic-key"}}, // Synthetic test credential. gitleaks:allow
		Denied:         map[string]time.Time{"retired": time.Unix(1, 0).UTC()},
		Evidence:       map[string]map[string]Evidence{"ds-flash": {"text": proof}},
		Stage:          &stageState{Settings: cloneSettings(settings), Connections: map[string]secretConnection{}, Evidence: map[string]map[string]Evidence{"ds-flash": {"text": proof}}},
		Probes:         map[string]probeRecord{"check": {View: ProbeView{Result: &proof}}},
		History:        []HistoryView{{Settings: cloneSettings(settings)}},
	}
	copied := cloneState(original)
	if copied.Connections["deepseek"].Connection.APIKey != "" || original.Connections["deepseek"].Connection.APIKey != "fixture" {
		t.Fatal("clone retained a transient factory key or changed the original handle")
	}
	before, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(copied)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("clone changed persisted metadata or fingerprint encoding")
	}
	copied.Settings.Profiles[0].Name = "changed"
	copied.Settings.Policy.LegacyHints["light"] = "complex"
	copied.Settings.Policy.Fallbacks["everyday"][0] = "changed"
	copied.Stage.Settings.Profiles[0].Model = "changed"
	copied.Stage.Evidence["ds-flash"]["text"] = Evidence{}
	copied.Evidence["ds-flash"]["text"] = Evidence{}
	copied.Probes["check"].View.Result.Fingerprint = "changed"
	copied.History[0].Settings.Profiles[0].Name = "changed"
	copied.Connections["deepseek"] = secretConnection{}
	delete(copied.Denied, "retired")
	unchanged, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, unchanged) {
		t.Fatal("candidate mutations leaked into the active state")
	}
}

package modelruntime

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/models"
)

func TestClassifierEvaluationActivationAndInvalidation(t *testing.T) {
	var bad atomic.Bool
	factory := func(p models.Profile, c Connection) (llm.Provider, error) {
		echo, _ := echoFactory(p, c)
		return providerFunc(func(ctx context.Context, r llm.Request) (llm.Response, error) {
			for i, f := range classifierFixtures {
				if r.Message == llm.ClassificationRequest(f.Message).Message {
					label := "simple"
					if f.Role == "complex" {
						label = "complex"
					}
					if bad.Load() && i == 4 {
						label = "simple"
					}
					return llm.Response{Content: label, FinishReason: "stop", Model: p.Model, ModelVerified: true, Usage: llm.Usage{InputTokens: 2, OutputTokens: 1}}, nil
				}
			}
			return echo.Complete(ctx, r)
		}), nil
	}
	repo := &memoryRepo{}
	m := newTestManager(t, repo, factory)
	t.Cleanup(func() { _ = m.Close(context.Background()) })
	settings := draftSettings()
	selector := settings.Profiles[0]
	selector.ID = "selector"
	selector.MaxOutputTokens = 512
	settings.Profiles = append(settings.Profiles, selector)
	settings.Policy.Complex = "ds-flash"
	settings.Policy.Classifier = models.Classifier{Enabled: true, Profile: selector.ID, TimeoutMS: 5000}
	stage, err := m.Stage(context.Background(), "admin", 0, settings, []ConnectionMutation{{Provider: "deepseek", APIKeyAction: "replace", APIKey: "test-key"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Activate(context.Background(), "admin", 0, stage.ID)
	assertCode(t, err, "evaluation_required")
	for _, id := range []string{"ds-flash", "selector"} {
		for _, kind := range []string{"text", "tools"} {
			v, probeErr := m.Probe(context.Background(), "admin", ProbeRequest{ExpectedRevision: 0, StageID: stage.ID, ProfileID: id, Kind: kind, IdempotencyKey: probeKey()})
			if probeErr != nil {
				t.Fatal(probeErr)
			}
			v = waitProbe(t, m, v.ID)
			if v.Status != "completed" {
				t.Fatalf("prerequisite failed: %+v", v)
			}
		}
	}
	request := ProbeRequest{ExpectedRevision: 0, StageID: stage.ID, ProfileID: "selector", Kind: "classifier", IdempotencyKey: probeKey()}
	v, err := m.Probe(context.Background(), "admin", request)
	if err != nil {
		t.Fatal(err)
	}
	v = waitProbe(t, m, v.ID)
	if v.Result == nil || !v.Result.Passed || v.Result.Correct != 12 || v.Result.ComplexCorrect != 8 || v.Result.ClassifierTimeoutMS != 5000 || v.Result.Usage.InputTokens != 24 {
		t.Fatalf("incorrect evidence: %+v", v.Result)
	}
	repeated, err := m.Probe(context.Background(), "admin", request)
	if err != nil || repeated.ID != v.ID {
		t.Fatal("paid evaluation duplicated")
	}
	view, err := m.Activate(context.Background(), "admin", 0, stage.ID)
	if err != nil || view.Revision != 1 {
		t.Fatalf("valid classifier not activated: %v", err)
	}
	restarted := newTestManager(t, repo, factory)
	t.Cleanup(func() { _ = restarted.Close(context.Background()) })
	r := llm.NewRoutingProvider(nil, nil)
	if publishErr := restarted.SetPublisher(r.SetProfileSnapshot); publishErr != nil {
		t.Fatal(publishErr)
	}
	if r.AcquireProfileSnapshot() == nil || restarted.Snapshot().ActiveRevision != 1 {
		t.Fatal("verified classifier did not survive restart")
	}
	// Deadline changes invalidate classifier evidence even when the provider and
	// profile parameters are unchanged. Other text/tool checks remain valid.
	settings.Policy.Classifier.TimeoutMS = 6000
	stage, err = m.Stage(context.Background(), "admin", 1, settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Activate(context.Background(), "admin", 1, stage.ID)
	assertCode(t, err, "evaluation_required")
	settings.Policy.Classifier.TimeoutMS = 5000
	stage, err = m.Stage(context.Background(), "admin", 1, settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A newly failed evaluation replaces the previous quality badge, rather than
	// leaving an old pass eligible after a missed complex request.
	bad.Store(true)
	future := time.Now().Add(61 * time.Second)
	m.now = func() time.Time { return future }
	request = ProbeRequest{ExpectedRevision: 1, StageID: stage.ID, ProfileID: "selector", Kind: "classifier", IdempotencyKey: fmt.Sprintf("%d:%s", future.UnixMilli(), randomID())}
	v, err = m.Probe(context.Background(), "admin", request)
	if err != nil {
		t.Fatal(err)
	}
	v = waitProbe(t, m, v.ID)
	if v.Result == nil || v.Result.Passed || v.Result.Correct != 11 || v.Result.ComplexCorrect != 7 || v.Result.ErrorCode != "classifier_quality_failed" {
		t.Fatalf("missed complex request passed: %+v", v.Result)
	}
	_, err = m.Activate(context.Background(), "admin", 1, stage.ID)
	assertCode(t, err, "evaluation_required")
	// Replacing a key also drops the old fingerprints, regardless of badge data.
	stage, err = m.Stage(context.Background(), "admin", 1, settings, []ConnectionMutation{{Provider: "deepseek", APIKeyAction: "replace", APIKey: "different-test-key"}}) // Synthetic test credential. gitleaks:allow
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Activate(context.Background(), "admin", 1, stage.ID)
	assertCode(t, err, "evaluation_required")
}
func TestClassifierEvaluationDeadlineFailsClosed(t *testing.T) {
	m := newTestManager(t, &memoryRepo{}, func(p models.Profile, c Connection) (llm.Provider, error) {
		return providerFunc(func(ctx context.Context, r llm.Request) (llm.Response, error) {
			<-ctx.Done()
			return llm.Response{Content: "complex"}, ctx.Err()
		}), nil
	})
	t.Cleanup(func() { _ = m.Close(context.Background()) })
	settings := draftSettings()
	settings.Profiles[0].MaxOutputTokens = 512
	settings.Policy.Classifier = models.Classifier{Profile: "ds-flash", TimeoutMS: 500}
	stage, err := m.Stage(context.Background(), "admin", 0, settings, []ConnectionMutation{{Provider: "deepseek", APIKeyAction: "replace", APIKey: "test-key"}})
	if err != nil {
		t.Fatal(err)
	}
	v, err := m.Probe(context.Background(), "admin", ProbeRequest{StageID: stage.ID, ProfileID: "ds-flash", Kind: "classifier", IdempotencyKey: probeKey()})
	if err != nil {
		t.Fatal(err)
	}
	v = waitProbe(t, m, v.ID)
	if v.Result == nil || v.Result.Passed || v.Result.Cases != 1 || v.Result.ErrorCode != "classifier_deadline_or_response_failed" {
		t.Fatalf("late quality response accepted: %+v", v.Result)
	}
	if len(m.Snapshot().Stage.Profiles[0].Evidence) != 1 || m.Snapshot().Stage.Profiles[0].Evidence[0].Passed {
		t.Fatal("timeout promoted evidence")
	}
	_, err = m.Probe(context.Background(), "ordinary", ProbeRequest{Kind: "classifier"})
	var coded *Error
	if !errors.As(err, &coded) {
		t.Fatal("unauthorized evaluation allowed")
	}
}

package httpfetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iulita-ai/iulita/internal/skill/interact"
)

// TestNativeConsumerUsesFactoryWithoutLLM covers plan §10.1/§15.4: another
// Go component obtains a scoped Fetcher via the Factory and runs typed plans
// with identical policy — no tool registry, no JSON-of-a-JSON, no LLM hop.
func TestNativeConsumerUsesFactoryWithoutLLM(t *testing.T) {
	proxy := newFakeProxy()
	srv := httptest.NewServer(http.HandlerFunc(proxy.handler))
	defer srv.Close()

	e, perr := NewEngine(EngineConfig{
		ProxyURL:  srv.URL,
		Limits:    testLimits(),
		Policy:    Policy{Enabled: true},
		Resolver:  fakeResolver{},
		Confirmer: &approvingConfirmer{},
		Artifacts: newMemArtifactStore(),
	})
	if perr != nil {
		t.Fatal(perr)
	}
	factory := NewFactory(e)

	fetcher, err := factory.ForCaller(context.Background(), Caller{ID: "weather-importer", Kind: CallerNativeSkill})
	if err != nil {
		t.Fatal(err)
	}

	// Read-only GET executes without any consent round-trip.
	plan, err := ParsePlan([]byte(getPlan))
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithScope(context.Background(), Scope{UserID: "u1", Mode: ModeInteractive, Caller: Caller{ID: "weather-importer", Kind: CallerNativeSkill}})
	res, err := fetcher.Run(ctx, plan)
	if err != nil || res.Outcome != OutcomeCompleted {
		t.Fatalf("native GET: %v %+v", err, res.Error)
	}

	// The same engine enforces the same consent on a native mutation call.
	post, _ := ParsePlan([]byte(`{"version":1,"request":{"id":"c","method":"DELETE","url":"http://api.example.test/v1/items/1"}}`))
	conf := &approvingConfirmer{}
	e2, _ := NewEngine(EngineConfig{ProxyURL: srv.URL, Limits: testLimits(), Policy: Policy{Enabled: true}, Resolver: fakeResolver{}, Confirmer: conf})
	f2 := NewFactory(e2)
	fetcher2, err := f2.ForCaller(ctx, Caller{ID: "weather-importer", Kind: CallerNativeSkill})
	if err != nil {
		t.Fatal(err)
	}
	res2, err := fetcher2.Run(ctx, post)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Outcome != OutcomeCompleted || len(conf.asked) != 1 || conf.asked[0] != interact.ConfirmMutation {
		t.Fatalf("native mutation bypassed consent: %s %v", res2.Outcome, conf.asked)
	}

	// A download above the threshold asks the same way (no bypass): a
	// declining consumer leaves no artifact.
	bigSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", "500")
		_, _ = w.Write([]byte(strings.Repeat("d", 500)))
	}))
	defer bigSrv.Close()
	e3, _ := NewEngine(EngineConfig{ProxyURL: bigSrv.URL, Limits: downloadLimits(), Policy: Policy{Enabled: true}, Resolver: fakeResolver{}, Confirmer: decliningConfirmer{}, Artifacts: newMemArtifactStore()})
	fetcher3, _ := NewFactory(e3).ForCaller(ctx, Caller{ID: "weather-importer", Kind: CallerNativeSkill})
	dl, _ := ParsePlan([]byte(downloadPlanFmt))
	res3, err := fetcher3.Run(ctx, dl)
	if err != nil {
		t.Fatal(err)
	}
	if res3.Outcome != OutcomeDeclined {
		t.Fatalf("native download declined path: %s", res3.Outcome)
	}
}

// TestFactoryGatesDisabledFeature verifies the runtime gate.
func TestFactoryGatesDisabledFeature(t *testing.T) {
	e, perr := NewEngine(EngineConfig{ProxyURL: "http://127.0.0.1:9", Policy: Policy{Enabled: false}})
	if perr != nil {
		t.Fatal(perr)
	}
	if _, err := NewFactory(e).ForCaller(context.Background(), Caller{ID: "x", Kind: CallerTool}); err == nil {
		t.Fatal("disabled feature issued a fetcher")
	}
	// Unknown caller kinds are rejected before any fetcher exists.
	e.SetEnabled(true)
	if _, err := NewFactory(e).ForCaller(context.Background(), Caller{ID: "x", Kind: "weird"}); err == nil {
		t.Fatal("unknown caller kind accepted")
	}
}

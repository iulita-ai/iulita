package main

import (
	"context"
	"fmt"

	"github.com/iulita-ai/iulita/internal/httpfetch"
)

// httpPlanFetcherAdapter adapts the strict engine to the skillmgr legacy
// proxy adapter: GET-only plans under the same proxy/policy. The legacy
// enable lifecycle (websearch manifest gates) stays with the caller — this
// adapter only exists when the engine itself was constructed (plan §10.3).
type httpPlanFetcherAdapter struct {
	factory *httpfetch.Factory
}

// Run executes a single-GET plan and returns the bounded inline text.
func (a *httpPlanFetcherAdapter) Run(ctx context.Context, planJSON []byte) (body string, status int, err error) {
	plan, err := httpfetch.ParsePlan(planJSON)
	if err != nil {
		return "", 0, err
	}
	fetcher, err := a.factory.ForCaller(ctx, httpfetch.Caller{ID: "webfetch_proxy", Kind: httpfetch.CallerNativeSkill})
	if err != nil {
		return "", 0, err
	}
	ctx = httpfetch.WithScope(ctx, httpfetch.Scope{
		Caller: httpfetch.Caller{ID: "webfetch_proxy", Kind: httpfetch.CallerNativeSkill},
		Mode:   httpfetch.ModeBackground, // read-only GETs; consent-needing flows are unavailable here
	})
	res, err := fetcher.Run(ctx, plan)
	if err != nil {
		return "", 0, err
	}
	if len(res.Steps) == 0 {
		return "", 0, fmt.Errorf("engine returned no steps")
	}
	st := res.Steps[0]
	if st.Body == nil {
		return "", st.Status, fmt.Errorf("no inline body (outcome %s)", st.Outcome)
	}
	return st.Body.Text, st.Status, nil
}

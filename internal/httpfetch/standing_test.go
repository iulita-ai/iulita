package httpfetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/skill/interact"
)

// memStanding is a fake StandingGrantStore with injectable coverage.
type memStanding struct {
	mu      sync.Mutex
	covered map[string]bool // owner + "|" + origin → covered
	saved   []string        // owner + "|" + standing + "|" + origin
	ttl     time.Duration
}

func newMemStanding() *memStanding {
	return &memStanding{covered: make(map[string]bool)}
}

func (m *memStanding) HasStandingGrant(_ context.Context, scope Scope, origin Origin) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.covered[scope.UserID+"|"+origin.String()], nil
}

func (m *memStanding) SaveStandingGrant(_ context.Context, scope Scope, origin Origin, standing string, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saved = append(m.saved, scope.UserID+"|"+standing+"|"+origin.String())
	m.ttl = time.Until(expiresAt)
	if standing == interact.StandingGlobal {
		// A global grant immediately covers the origin for this fake.
		m.covered[scope.UserID+"|any"] = true
	}
	m.covered[scope.UserID+"|"+origin.String()] = true
	return nil
}

// standingConfirmer approves with a standing choice.
type standingConfirmer struct {
	mu       sync.Mutex
	asked    int
	standing string
}

func (c *standingConfirmer) Confirm(_ context.Context, _ interact.Confirmation, expect interact.Actor) (interact.Decision, error) {
	c.mu.Lock()
	c.asked++
	c.mu.Unlock()
	decision := interact.Decision{Approved: true, Actor: expect}
	decision.Standing = c.standing
	return decision, nil
}

const standingPostPlan = `{"version":1,"request":{"id":"m","method":"POST","url":"http://api.example.test/v1/items","body":{"type":"json","value":{"x":1}},"response":{"mode":"inline"}}}`

func TestStandingOriginGrantSkipsNextConsent(t *testing.T) {
	proxy := newFakeProxy()
	srv := httptest.NewServer(http.HandlerFunc(proxy.handler))
	defer srv.Close()

	standing := newMemStanding()
	conf := &standingConfirmer{standing: interact.StandingOrigin}
	e, perr := NewEngine(EngineConfig{
		ProxyURL: srv.URL, Limits: testLimits(), Policy: Policy{Enabled: true},
		Resolver: fakeResolver{}, Confirmer: conf, Standing: standing,
	})
	if perr != nil {
		t.Fatal(perr)
	}

	// First POST: asks, user picks "always for this origin" → grant saved.
	res := runPlan(t, e, standingPostPlan)
	if res.Outcome != OutcomeCompleted {
		t.Fatalf("first POST: %s (%+v)", res.Outcome, res.Error)
	}
	if conf.asked != 1 {
		t.Fatalf("asked %d times on first call", conf.asked)
	}
	if len(standing.saved) != 1 || standing.saved[0] != "u1|origin|http://api.example.test:80" {
		t.Fatalf("saved grants = %v", standing.saved)
	}

	// Second POST to the same origin: no question, straight through.
	res2 := runPlan(t, e, standingPostPlan)
	if res2.Outcome != OutcomeCompleted {
		t.Fatalf("second POST: %s (%+v)", res2.Outcome, res2.Error)
	}
	if conf.asked != 1 {
		t.Fatalf("standing grant did not skip consent (asked %d)", conf.asked)
	}
}

func TestStandingGrantDoesNotCoverOtherOriginsOrUsers(t *testing.T) {
	proxy := newFakeProxy()
	srv := httptest.NewServer(http.HandlerFunc(proxy.handler))
	defer srv.Close()

	standing := newMemStanding()
	// Grant exists for u1@origin-a.
	standing.covered["u1|http://a.example.test:80"] = true
	conf := &standingConfirmer{}
	e, _ := NewEngine(EngineConfig{
		ProxyURL: srv.URL, Limits: testLimits(), Policy: Policy{Enabled: true},
		Resolver: fakeResolver{}, Confirmer: conf, Standing: standing,
	})

	// Same user, different origin → asks again.
	res := runPlan(t, e, `{"version":1,"request":{"id":"m","method":"POST","url":"http://b.example.test/v1"}}`)
	if res.Outcome != OutcomeCompleted || conf.asked != 1 {
		t.Fatalf("cross-origin bypass: outcome=%s asked=%d", res.Outcome, conf.asked)
	}

	// Different user, granted origin → asks again.
	other := WithScope(context.Background(), Scope{UserID: "u2", Mode: ModeInteractive, Caller: Caller{ID: "t", Kind: CallerTool}})
	plan, _ := ParsePlan([]byte(`{"version":1,"request":{"id":"m","method":"POST","url":"http://a.example.test/v1"}}`))
	res2, err := e.Run(other, plan)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Outcome != OutcomeCompleted || conf.asked != 2 {
		t.Fatalf("cross-user bypass: outcome=%s asked=%d", res2.Outcome, conf.asked)
	}
}

func TestStandingNeverCoversDownloads(t *testing.T) {
	// Even a GLOBAL standing grant must not auto-approve a >threshold
	// download: the byte-limit consent always reaches a human.
	bigSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", "500")
		_, _ = w.Write([]byte(repeatStr("d", 500)))
	}))
	defer bigSrv.Close()

	standing := &globalStanding{}
	conf := &standingConfirmer{}
	e, _ := NewEngine(EngineConfig{
		ProxyURL: bigSrv.URL, Limits: downloadLimits(), Policy: Policy{Enabled: true},
		Resolver: fakeResolver{}, Confirmer: conf, Standing: standing,
		Artifacts: newMemArtifactStore(),
	})
	res := runPlan(t, e, downloadPlanFmt)
	if res.Outcome != OutcomeCompleted {
		t.Fatalf("download with global standing: %s (%+v)", res.Outcome, res.Error)
	}
	if conf.asked != 1 {
		t.Fatalf("global standing skipped the large-download consent (asked=%d)", conf.asked)
	}
}

// globalStanding covers every origin.
type globalStanding struct{}

func (globalStanding) HasStandingGrant(_ context.Context, scope Scope, _ Origin) (bool, error) {
	return scope.UserID != "", nil
}
func (globalStanding) SaveStandingGrant(context.Context, Scope, Origin, string, time.Time) error {
	return nil
}

func repeatStr(s string, n int) string {
	out := make([]byte, 0, n*len(s))
	for len(out) < n {
		out = append(out, s...)
	}
	return string(out[:n])
}

package httpfetch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/web"
)

// Fetcher executes a validated plan. The only public entry point; no access
// to the underlying http.Client, transport, or policy bypass is exposed.
type Fetcher interface {
	Run(ctx context.Context, plan Plan) (RunResult, error)
}

// Engine is the shared strict HTTP executor. One instance per process,
// created by host wiring; per-run state lives in runState.
type Engine struct {
	enabled   atomic.Bool
	transport *http.Transport
	client    *http.Client
	limits    Limits
	policy    Policy
	proxySafe string // scheme+host of configured proxy, for status only

	resolver          web.Resolver
	creds             CredentialResolver
	confirmer         Confirmer
	artifacts         ArtifactStore
	rawArtifacts      bool // include_raw_artifact may store owner-private copies
	standing          StandingGrantStore
	standingOriginTTL time.Duration
	standingGlobalTTL time.Duration
	metrics           MetricsHooks
	log               *zap.Logger

	mu            sync.Mutex
	userTransfer  map[string]int // per-user active transfers
	transferSlots chan struct{}  // global transfer permits
	writerSlots   chan struct{}  // global download writer permits
	pendingSlots  chan struct{}  // paused-stream confirmation permits
}

// EngineConfig wires the engine. Everything is host-provided; nothing is
// derived from model input.
type EngineConfig struct {
	ProxyURL          string
	Limits            Limits
	Policy            Policy
	Resolver          web.Resolver // nil → net.DefaultResolver
	Credentials       CredentialResolver
	Confirmer         Confirmer
	Artifacts         ArtifactStore
	Standing          StandingGrantStore // nil = standing approvals unavailable
	StandingOriginTTL time.Duration      // "always for this origin" lifetime (default 30d)
	StandingGlobalTTL time.Duration      // "always" lifetime (default 7d)
	RawArtifacts      bool               // allow include_raw_artifact storage (needs Artifacts)
	Metrics           MetricsHooks
	Logger            *zap.Logger
}

// NewEngine builds the proxy-only engine. A missing/invalid proxy is a typed
// error — the rest of Iulita keeps running without the HTTP tool.
func NewEngine(cfg EngineConfig) (*Engine, *ProxyError) {
	limits := cfg.Limits
	if limits.ConsentThresholdBytes == 0 {
		limits = DefaultLimits()
	}
	transport, perr := NewProxyOnlyTransport(cfg.ProxyURL, limits)
	if perr != nil {
		return nil, perr
	}
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	e := &Engine{
		transport: transport,
		client: &http.Client{
			Transport:     transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			// No Client.Timeout: per-step deadlines and idle timers are
			// managed explicitly by the engine.
		},
		limits:            limits,
		policy:            cfg.Policy,
		proxySafe:         proxySchemeHost(cfg.ProxyURL),
		resolver:          cfg.Resolver,
		creds:             cfg.Credentials,
		confirmer:         cfg.Confirmer,
		artifacts:         cfg.Artifacts,
		standing:          cfg.Standing,
		standingOriginTTL: cfg.StandingOriginTTL,
		standingGlobalTTL: cfg.StandingGlobalTTL,
		rawArtifacts:      cfg.RawArtifacts,
		metrics:           cfg.Metrics,
		log:               logger,
		userTransfer:      make(map[string]int),
		transferSlots:     make(chan struct{}, limits.MaxTransfersGlobal),
		writerSlots:       make(chan struct{}, limits.MaxDownloadWriters),
		pendingSlots:      make(chan struct{}, limits.MaxPendingGlobal),
	}
	e.enabled.Store(cfg.Policy.Enabled)
	if e.metrics == nil {
		e.metrics = noopMetrics{}
	}
	return e, nil
}

// ProxyStatus reports proxy availability and scheme without the address or
// credentials (plan §7.1: status never prints the proxy URL with userinfo).
func (e *Engine) ProxyStatus() (available bool, scheme string) {
	return e.proxySafe != "", e.proxySafe
}

// Enabled reports the runtime feature state (checked again per step).
func (e *Engine) Enabled() bool { return e.enabled.Load() }

// SetEnabled toggles the feature; in-flight requests are canceled via their
// contexts, and new steps fail closed.
func (e *Engine) SetEnabled(v bool) { e.enabled.Store(v) }

// Limits exposes the effective limits (read-only use).
func (e *Engine) Limits() Limits { return e.limits }

func proxySchemeHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return ""
	}
	return u.Scheme
}

// Factory creates caller-scoped fetchers. Host adapters call ForCaller with
// a trusted Caller; the model never controls this value.
type Factory struct {
	engine *Engine
}

// NewFactory wraps an engine.
func NewFactory(e *Engine) *Factory { return &Factory{engine: e} }

// SetEnabled toggles the runtime feature state; in-flight requests are
// canceled via their contexts, and new steps fail closed. Host wiring calls
// this from the config-reload path (skills.http.enabled).
func (f *Factory) SetEnabled(v bool) {
	if f != nil && f.engine != nil {
		f.engine.SetEnabled(v)
	}
}

// Enabled mirrors the runtime feature state.
func (f *Factory) Enabled() bool { return f != nil && f.engine != nil && f.engine.Enabled() }

// ForCaller validates the caller and returns a scoped Fetcher. The fetcher
// re-checks policy on every step, so a mid-run disable stops the chain.
func (f *Factory) ForCaller(ctx context.Context, caller Caller) (Fetcher, error) {
	if f.engine == nil {
		return nil, fmt.Errorf("http engine not initialized")
	}
	if !f.engine.Enabled() {
		return nil, fmt.Errorf("http feature disabled")
	}
	switch caller.Kind {
	case CallerTool, CallerNativeSkill, CallerSystem:
	default:
		return nil, fmt.Errorf("caller kind %q not permitted", caller.Kind)
	}
	if caller.ID == "" {
		return nil, fmt.Errorf("caller ID required")
	}
	return f.engine, nil
}

// Engine as Fetcher.
var _ Fetcher = (*Engine)(nil)

// runState carries everything scoped to one Run: variables with sensitivity
// and origin provenance, the per-run cookie jar, grants, and budgets.
type runState struct {
	opID          string
	fingerprint   string
	currentStep   string
	vars          map[string]varValue
	jar           *cookieJar
	internal      map[string]*stepInternal // step id → final response metadata
	insecureOK    map[string]bool          // origin approved for plaintext secrets
	egressOK      map[string]bool          // "srcOrigin→dstOrigin" approved
	chainUsed     time.Duration            // non-download active budget spent
	secretOrigins map[string]Origin        // inline secret → first-seen origin
}

type stepInternal struct {
	finalURL *url.URL    // URL of the final response (after redirects)
	headers  http.Header // final response headers
	status   int
}

type varValue struct {
	Value     any
	Sensitive bool
	Origin    Origin
	Declared  VarType
}

// Run executes a plan. Non-success outcomes are returned as structured
// RunResult values with a nil error; the Go error is reserved for internal
// failures that cannot produce a result envelope.
func (e *Engine) Run(ctx context.Context, plan Plan) (RunResult, error) {
	opID := NewOperationID()
	result := RunResult{Version: 1, OperationID: opID, ExternalContent: true}

	if !e.Enabled() {
		result.Outcome = OutcomePolicyDenied
		result.Error = &ErrorInfo{Outcome: OutcomePolicyDenied, Code: "http_disabled", Message: "HTTP tool is disabled"}
		return result, nil
	}

	if err := e.ValidatePlan(plan); err != nil {
		result.Outcome = OutcomeValidationError
		result.Error = errorInfoFromValidation(err)
		return result, nil
	}

	scope, _ := ScopeFrom(ctx)
	steps := plan.Steps
	if plan.Request != nil {
		r := *plan.Request
		if r.ID == "" {
			r.ID = defaultStepID
		}
		steps = []Request{r}
	}

	state := &runState{
		opID:          opID,
		fingerprint:   Fingerprint(plan),
		vars:          make(map[string]varValue),
		internal:      make(map[string]*stepInternal),
		insecureOK:    make(map[string]bool),
		egressOK:      make(map[string]bool),
		secretOrigins: make(map[string]Origin),
	}
	if plan.CookieMode == CookieModeChain && len(steps) > 1 {
		state.jar = newCookieJar()
	}

	wallCtx, wallCancel := context.WithTimeout(ctx, e.limits.WallTimeout)
	defer wallCancel()

	stopReason := ""
	for i := range steps {
		step := steps[i]
		sr := e.runStep(wallCtx, step, state, scope)
		result.Steps = append(result.Steps, sr)

		if sr.Outcome == OutcomeCompleted || sr.Outcome == OutcomeHTTPError {
			if sr.Outcome == OutcomeHTTPError && !step.ContinueOnError {
				stopReason = fmt.Sprintf("step %s: status %d outside expect_status", step.ID, sr.Status)
				break
			}
			continue
		}
		stopReason = fmt.Sprintf("step %s: %s", step.ID, sr.Outcome)
		break
	}

	result.Outcome = e.aggregateOutcome(result.Steps)
	if stopReason != "" {
		result.StopReason = stopReason
	}
	// Mirror the failing step's error info to the top level so callers never
	// have to guess which nested field carries it.
	if result.Outcome != OutcomeCompleted && result.Error == nil && len(result.Steps) > 0 {
		for i := len(result.Steps) - 1; i >= 0; i-- {
			if result.Steps[i].Error != nil {
				result.Error = result.Steps[i].Error
				break
			}
		}
	}
	return result, nil
}

// aggregateOutcome derives the run outcome from step outcomes.
func (e *Engine) aggregateOutcome(steps []StepResult) Outcome {
	if len(steps) == 0 {
		return OutcomeValidationError
	}
	last := steps[len(steps)-1]
	if last.Outcome == OutcomeSkipped {
		// A skipped tail means an earlier step failed.
		for i := len(steps) - 1; i >= 0; i-- {
			if steps[i].Outcome != OutcomeSkipped {
				return steps[i].Outcome
			}
		}
	}
	return last.Outcome
}

// errorInfoFromValidation converts a ValidationError into result metadata.
func errorInfoFromValidation(err error) *ErrorInfo {
	var ve *ValidationError
	if errors.As(err, &ve) {
		return &ErrorInfo{Outcome: OutcomeValidationError, Code: "validation", Message: ve.Error(), StepID: ve.StepID}
	}
	return &ErrorInfo{Outcome: OutcomeValidationError, Code: "validation", Message: err.Error()}
}

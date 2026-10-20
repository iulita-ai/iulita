package remoteaccess

import (
	"context"
	"errors"
	"sync"
	"time"
)

// TerminalOptions defines one explicitly approved human terminal session.
type TerminalOptions struct {
	Rows           int    `json:"rows"`
	Cols           int    `json:"cols"`
	Term           string `json:"term"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

// TerminalRequest binds terminal consent to the route and immutable dimensions.
type TerminalRequest struct {
	RouteID          string          `json:"route_id"`
	Options          TerminalOptions `json:"options"`
	ApprovalID       string          `json:"approval_id,omitempty"`
	ExpectedPlanHash string          `json:"expected_plan_hash,omitempty"`
}

// Terminal identifies the auditable execution without exposing credentials.
type Terminal struct {
	ExecutionID string `json:"execution_id"`
	RouteID     string `json:"route_id"`
}

func terminalOperation(in TerminalRequest) (ExecuteRequest, error) {
	p := PTYRequest{Rows: in.Options.Rows, Cols: in.Options.Cols, Term: in.Options.Term, Timeout: time.Duration(in.Options.TimeoutSeconds) * time.Second}
	if err := validatePTY(&p); err != nil {
		return ExecuteRequest{}, ErrDenied
	}
	options := TerminalOptions{Rows: p.Rows, Cols: p.Cols, Term: p.Term, TimeoutSeconds: int(p.Timeout / time.Second)}
	return ExecuteRequest{RouteID: in.RouteID, Operation: "terminal", Capability: "terminal", Command: "Interactive human SSH terminal: full account privileges for this session; remote output remains outside model context.", TimeoutSeconds: options.TimeoutSeconds, Terminal: &options, ApprovalID: in.ApprovalID, ExpectedPlanHash: in.ExpectedPlanHash}, nil
}

// PreviewTerminal freezes one terminal session for explicit owner approval.
func (s *Service) PreviewTerminal(sc Scope, in TerminalRequest) (Approval, error) {
	if sc.CallerID != "dashboard" {
		return Approval{}, ErrDenied
	}
	req, err := terminalOperation(in)
	if err != nil {
		return Approval{}, err
	}
	return s.Preview(sc, req)
}

type terminalOpening struct {
	meta    Terminal
	session PTYSession
}
type terminalDispatch struct {
	base    PTYTransport
	options TerminalOptions
	id      string
	route   string
	opened  chan terminalOpening
	once    sync.Once
}

// Run keeps the durable execution open until the human terminal closes.
func (t *terminalDispatch) Run(ctx context.Context, r TransportRequest) (TransportResult, error) {
	session, err := t.base.OpenPTY(ctx, PTYRequest{Endpoints: r.Endpoints, Rows: t.options.Rows, Cols: t.options.Cols, Term: t.options.Term, Timeout: time.Duration(t.options.TimeoutSeconds) * time.Second})
	if err != nil {
		return TransportResult{Dispatched: true, ErrorCode: "terminal_open_unknown"}, errors.New("terminal connection failed")
	}
	defer closeTransport(session)
	t.once.Do(func() {
		t.opened <- terminalOpening{meta: Terminal{ExecutionID: t.id, RouteID: t.route}, session: session}
	})
	if err = session.Wait(); err != nil {
		return TransportResult{Dispatched: true, ErrorCode: "terminal_outcome_unknown"}, errors.New("terminal closed without confirmed exit")
	}
	zero := 0
	return TransportResult{Dispatched: true, ExitCode: &zero}, nil
}

// OpenTerminal consumes one durable approval. There is no reconnect or replay.
func (s *Service) OpenTerminal(ctx context.Context, sc Scope, in TerminalRequest) (Terminal, PTYSession, error) {
	if sc.CallerID != "dashboard" || in.ApprovalID == "" || in.ExpectedPlanHash == "" {
		return Terminal{}, nil, ErrDenied
	}
	req, err := terminalOperation(in)
	if err != nil {
		return Terminal{}, nil, err
	}
	base, ok := s.transport.(PTYTransport)
	if !ok {
		return Terminal{}, nil, ErrDenied
	}
	dispatch := &terminalDispatch{base: base, options: *req.Terminal, route: req.RouteID, opened: make(chan terminalOpening, 1)}
	done := make(chan error, 1)
	go func() {
		_, runErr := s.executeWithTransport(ctx, sc, req, func(_ context.Context, a Approval) (bool, error) {
			dispatch.id = a.ExecutionID
			return a.PlanHash == in.ExpectedPlanHash, nil
		}, dispatch)
		done <- runErr
	}()
	select {
	case opened := <-dispatch.opened:
		return opened.meta, opened.session, nil
	case err := <-done:
		if err == nil {
			err = ErrDenied
		}
		return Terminal{}, nil, err
	case <-ctx.Done():
		return Terminal{}, nil, ctx.Err()
	}
}

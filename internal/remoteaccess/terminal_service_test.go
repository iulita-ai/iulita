package remoteaccess

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type servicePTYTransport struct {
	opens   atomic.Int32
	session *servicePTY
}

func (t *servicePTYTransport) Run(context.Context, TransportRequest) (TransportResult, error) {
	return TransportResult{}, errors.New("unexpected exec")
}
func (t *servicePTYTransport) OpenPTY(ctx context.Context, _ PTYRequest) (PTYSession, error) {
	t.opens.Add(1)
	t.session = &servicePTY{done: make(chan struct{})}
	go func() { <-ctx.Done(); t.session.Close() }()
	return t.session, nil
}

type servicePTY struct {
	once sync.Once
	done chan struct{}
}

func (p *servicePTY) Read([]byte) (int, error)    { <-p.done; return 0, io.EOF }
func (p *servicePTY) Write(b []byte) (int, error) { return len(b), nil }
func (p *servicePTY) Resize(int, int) error       { return nil }
func (p *servicePTY) Wait() error                 { <-p.done; return errors.New("cancelled unknown") }
func (p *servicePTY) Close() error                { p.once.Do(func() { close(p.done) }); return nil }
func terminalServiceFixture(t *testing.T) (*Service, Scope, TerminalRequest, *servicePTYTransport) {
	t.Helper()
	s, sc, r, _ := setupService(t)
	sc.CallerID = "dashboard"
	if _, err := s.PutGrant(sc, Grant{RouteID: r.RouteID, CallerID: sc.CallerID, Capability: "terminal", Enabled: true, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	fake := &servicePTYTransport{}
	s.transport = fake
	req := TerminalRequest{RouteID: r.RouteID, Options: TerminalOptions{Rows: 24, Cols: 80, Term: "xterm-256color", TimeoutSeconds: 3600}}
	preview, err := s.PreviewTerminal(sc, req)
	if err != nil {
		t.Fatal(err)
	}
	req.ApprovalID = preview.ApprovalID
	req.ExpectedPlanHash = preview.PlanHash
	return s, sc, req, fake
}
func TestTerminalServiceSingleUseAndDisable(t *testing.T) {
	s, sc, req, tr := terminalServiceFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	meta, session, err := s.OpenTerminal(ctx, sc, req)
	if err != nil || meta.ExecutionID == "" {
		t.Fatalf("open %+v %v", meta, err)
	}
	defer session.Close()
	if _, _, err = s.OpenTerminal(ctx, sc, req); err == nil || tr.opens.Load() != 1 {
		t.Fatal("terminal reattached or replayed")
	}
	if err = s.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-tr.session.done:
	case <-time.After(time.Second):
		t.Fatal("disable did not close terminal")
	}
	deadline := time.Now().Add(time.Second)
	for {
		execution := s.Snapshot(sc).Executions[meta.ExecutionID]
		if execution.State == "unknown_outcome" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("closed terminal false outcome %+v", execution)
		}
		time.Sleep(time.Millisecond)
	}
	b, err := json.Marshal(s.Snapshot(sc))
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 {
		t.Fatal("missing metadata")
	}
}
func TestTerminalServiceRejectsChangedPlanAndWrongActor(t *testing.T) {
	s, sc, req, tr := terminalServiceFixture(t)
	changed := req
	changed.Options.Cols = 120
	if _, _, err := s.OpenTerminal(context.Background(), sc, changed); err == nil {
		t.Fatal("changed terminal plan accepted")
	}
	other := sc
	other.OwnerID = "foreign"
	if _, _, err := s.OpenTerminal(context.Background(), other, req); err == nil {
		t.Fatal("foreign terminal consent accepted")
	}
	delegated := sc
	delegated.CallerID = "delegate"
	if _, _, err := s.OpenTerminal(context.Background(), delegated, req); err == nil {
		t.Fatal("agent terminal accepted")
	}
	if tr.opens.Load() != 0 {
		t.Fatal("rejected terminal reached transport")
	}
}

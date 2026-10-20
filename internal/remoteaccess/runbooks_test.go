package remoteaccess

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func TestStandingRunbookScopesAndRevision(t *testing.T) {
	s, sc, req, tr := setupService(t)
	b, e := s.PutRunbook(sc, Runbook{RouteID: req.RouteID, Command: "printf %s {{path}}", Parameters: map[string]Parameter{"path": {Values: []string{"/var", "a'; touch /tmp/pwn; '"}}}, Enabled: true})
	if e != nil {
		t.Fatal(e)
	}
	sc.CallerID = "delegate"
	_, e = s.ExecuteRunbook(context.Background(), sc, RunbookRequest{RunbookID: b.ID, Parameters: map[string]string{"path": "/var"}})
	if e == nil {
		t.Fatal("missing standing grant accepted")
	}
	_, e = s.PutGrant(sc, Grant{RouteID: req.RouteID, CallerID: "delegate", Capability: "runbook", RunbookID: b.ID, MaxUses: 1, Enabled: true, ExpiresAt: time.Now().Add(time.Hour)})
	if e != nil {
		t.Fatal(e)
	}
	x, e := s.ExecuteRunbook(context.Background(), sc, RunbookRequest{RunbookID: b.ID, Parameters: map[string]string{"path": "a'; touch /tmp/pwn; '"}})
	if e != nil || x.State != "completed" {
		t.Fatalf("%+v %v", x, e)
	}
	a, e := s.Artifact(sc, x.ArtifactID)
	if e != nil || a.Command != "printf %s 'a'\"'\"'; touch /tmp/pwn; '\"'\"''" {
		t.Fatalf("unsafe rendering %q %v", a.Command, e)
	}
	_, e = s.ExecuteRunbook(context.Background(), sc, RunbookRequest{RunbookID: b.ID, Parameters: map[string]string{"path": "/var"}})
	if e == nil || tr.calls != 1 {
		t.Fatal("use limit bypass")
	}
	b, e = s.PutRunbook(sc, b)
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.ExecuteRunbook(context.Background(), sc, RunbookRequest{RunbookID: b.ID, Parameters: map[string]string{"path": "/var"}})
	if e == nil {
		t.Fatal("old revision authorized")
	}
}
func TestFileScopeAndApproval(t *testing.T) {
	s, sc, req, tr := setupService(t)
	_, e := s.PutGrant(sc, Grant{RouteID: req.RouteID, CallerID: sc.CallerID, Capability: "file_get", FileRoot: "/data", Enabled: true, ExpiresAt: time.Now().Add(time.Hour)})
	if e != nil {
		t.Fatal(e)
	}
	r := ExecuteRequest{RouteID: req.RouteID, Operation: "file_get", File: &FileRequest{Root: "/data", Path: "report.txt"}}
	if _, e = s.ExecuteOperation(context.Background(), sc, r, nil); e == nil {
		t.Fatal("file operation omitted consent")
	}
	r.File.Root = "/etc"
	if _, e = s.ExecuteOperation(context.Background(), sc, r, func(context.Context, Approval) (bool, error) { return true, nil }); e == nil {
		t.Fatal("root grant bypass")
	}
	r.File.Root = "/data"
	r.File.Path = "../etc/shadow"
	if _, e = s.ExecuteOperation(context.Background(), sc, r, func(context.Context, Approval) (bool, error) { return true, nil }); e == nil {
		t.Fatal("traversal accepted")
	}
	if tr.calls != 0 {
		t.Fatal("unauthorized dispatch")
	}
}
func TestExecuteCannotSmuggleTypedOperation(t *testing.T) {
	s, sc, r, tr := setupService(t)
	for _, mutate := range []func(*ExecuteRequest){func(r *ExecuteRequest) { r.Operation = "background_start" }, func(r *ExecuteRequest) { r.File = &FileRequest{} }, func(r *ExecuteRequest) { r.Background = &BackgroundRequest{} }, func(r *ExecuteRequest) { r.RunbookID = "fake" }} {
		q := r
		mutate(&q)
		if _, e := s.Execute(context.Background(), sc, q, func(context.Context, Approval) (bool, error) { return true, nil }); e == nil {
			t.Fatal("typed capability bypass")
		}
	}
	if tr.calls != 0 {
		t.Fatal("unexpected dispatch")
	}
}
func TestScheduledFiringDurableNoReplay(t *testing.T) {
	s, sc, r, tr := setupService(t)
	b, e := s.PutRunbook(sc, Runbook{RouteID: r.RouteID, Command: "true", Enabled: true})
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.PutGrant(sc, Grant{RouteID: r.RouteID, CallerID: "scheduler", Capability: "runbook", RunbookID: b.ID, MaxUses: 5, Enabled: true, ExpiresAt: time.Now().Add(time.Hour)})
	if e != nil {
		t.Fatal(e)
	}
	in := RunbookRequest{RunbookID: b.ID}
	first, e := s.ExecuteScheduledRunbook(context.Background(), sc.OwnerID, "job-1-task-123", in)
	if e != nil {
		t.Fatal(e)
	}
	second, e := s.ExecuteScheduledRunbook(context.Background(), sc.OwnerID, "job-1-task-123", in)
	if e != nil || second.ID != first.ID || tr.calls != 1 {
		t.Fatalf("replayed %+v %v calls%d", second, e, tr.calls)
	}
	in.TimeoutSeconds = 200
	if _, e = s.ExecuteScheduledRunbook(context.Background(), sc.OwnerID, "job-1-task-123", in); e == nil {
		t.Fatal("firing content substitution accepted")
	}
	s.mu.Lock()
	s.data.ScheduledRuns[sc.OwnerID+":job-1-task-124"] = ScheduledRun{OwnerID: sc.OwnerID, RequestHash: s.data.ScheduledRuns[sc.OwnerID+":job-1-task-123"].RequestHash, State: "claimed"}
	s.mu.Unlock()
	in.TimeoutSeconds = 0
	x, e := s.ExecuteScheduledRunbook(context.Background(), sc.OwnerID, "job-1-task-124", in)
	if e != nil || x.State != "unknown_outcome" || tr.calls != 1 {
		t.Fatal("crash claim replayed")
	}
}
func TestParameterizedTemplateRejectsShellContexts(t *testing.T) {
	s, sc, r, _ := setupService(t)
	for _, command := range []string{`printf "{{p}}"`, `printf '{{p}}'`, `printf $(echo {{p}})`, "cat <<EOF\n{{p}}\nEOF", `printf x{{p}}`, `{{p}} arg`, `echo {{p}}; true`, `echo \` + "`" + `{{p}}` + "`"} {
		if _, e := s.PutRunbook(sc, Runbook{RouteID: r.RouteID, Command: command, Parameters: map[string]Parameter{"p": {Values: []string{"$(touch /tmp/pwn)"}}}, Enabled: true}); e == nil {
			t.Fatalf("unsafe template accepted %q", command)
		}
	}
}
func TestRecoveryControlsRemainAvailableAtQuotaAndDisabled(t *testing.T) {
	s, sc, r, tr := setupService(t)
	_, e := s.PutGrant(sc, Grant{RouteID: r.RouteID, CallerID: sc.CallerID, Capability: "background_cancel", FileRoot: "/jobs", Enabled: true, ExpiresAt: time.Now().Add(time.Hour)})
	if e != nil {
		t.Fatal(e)
	}
	s.mu.Lock()
	for i := 0; i < 1000; i++ {
		id := fmt.Sprintf("old-%d", i)
		s.data.Executions[id] = Execution{ID: id, OwnerID: sc.OwnerID, State: "completed", FinishedAt: time.Now()}
	}
	s.data.Jobs["0123456789abcdef"] = RemoteJob{ID: "0123456789abcdef", OwnerID: sc.OwnerID, RouteID: r.RouteID, Root: "/jobs", State: "running"}
	s.mu.Unlock()
	if e = s.SetEnabled(false); e != nil {
		t.Fatal(e)
	}
	request := ExecuteRequest{RouteID: r.RouteID, Operation: "background_cancel", Background: &BackgroundRequest{Root: "/jobs", JobID: "0123456789abcdef"}}
	for i := 0; i < 5; i++ {
		if _, e = s.ExecuteOperation(context.Background(), sc, request, func(context.Context, Approval) (bool, error) { return true, nil }); e != nil {
			t.Fatalf("recovery denied %v", e)
		}
	}
	if tr.calls != 5 {
		t.Fatal("recovery did not dispatch")
	}
	s.mu.Lock()
	count := len(s.data.Executions)
	s.mu.Unlock()
	if count > 1002 {
		t.Fatal("unbounded recovery retention")
	}
}
func TestPrincipalCheckerDeniesAdmissionAndCancelsActive(t *testing.T) {
	s, sc, r, tr := setupService(t)
	var allowed atomic.Bool
	s.SetPrincipalChecker(func(context.Context, string) bool { return allowed.Load() })
	if _, e := s.Execute(context.Background(), sc, r, func(context.Context, Approval) (bool, error) { return true, nil }); e == nil || tr.calls != 0 {
		t.Fatal("disabled principal admitted")
	}
	allowed.Store(true)
	started := make(chan struct{})
	tr.run = func(ctx context.Context) (TransportResult, error) {
		close(started)
		<-ctx.Done()
		return TransportResult{Dispatched: true}, ctx.Err()
	}
	done := make(chan Execution, 1)
	go func() {
		x, _ := s.Execute(context.Background(), sc, r, func(context.Context, Approval) (bool, error) { return true, nil })
		done <- x
	}()
	<-started
	allowed.Store(false)
	select {
	case x := <-done:
		if x.State != "unknown_outcome" {
			t.Fatalf("false cancellation guarantee %+v", x)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("principal revoke did not cancel active transport")
	}
}
func TestRestartReconcilesRecoveryApprovalsAndAllowsNewWork(t *testing.T) {
	s, sc, r, tr := setupService(t)
	if _, e := s.PutGrant(sc, Grant{RouteID: r.RouteID, CallerID: sc.CallerID, Capability: "background_cancel", FileRoot: "/jobs", Enabled: true, ExpiresAt: time.Now().Add(time.Hour)}); e != nil {
		t.Fatal(e)
	}
	s.mu.Lock()
	for i := 0; i < 1000; i++ {
		id := fmt.Sprintf("history-%d", i)
		s.data.Executions[id] = Execution{ID: id, OwnerID: sc.OwnerID, State: "completed", FinishedAt: time.Now()}
	}
	for _, id := range []string{"pending-recovery-one", "pending-recovery-two"} {
		s.data.Executions[id] = Execution{ID: id, OwnerID: sc.OwnerID, State: "awaiting_approval", Operation: "background_cancel"}
	}
	s.data.Jobs["0123456789abcdef"] = RemoteJob{ID: "0123456789abcdef", OwnerID: sc.OwnerID, RouteID: r.RouteID, Root: "/jobs", State: "running"}
	e := s.save()
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := NewService(s.path, s.vault, tr)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	snap := reopened.Snapshot(sc)
	for _, id := range []string{"pending-recovery-one", "pending-recovery-two"} {
		x := snap.Executions[id]
		if x.State != "cancelled" || x.FinishedAt.IsZero() {
			t.Fatal("pending recovery not reconciled")
		}
	}
	if e = reopened.SetEnabled(false); e != nil {
		t.Fatal(e)
	}
	req := ExecuteRequest{RouteID: r.RouteID, Operation: "background_cancel", Background: &BackgroundRequest{Root: "/jobs", JobID: "0123456789abcdef"}}
	if _, e = reopened.ExecuteOperation(context.Background(), sc, req, func(context.Context, Approval) (bool, error) { return true, nil }); e != nil {
		t.Fatalf("restart bricked recovery: %v", e)
	}
}
func TestExecutionHistoryRollsWithoutReplayingOldApproval(t *testing.T) {
	s, sc, r, tr := setupService(t)
	a, e := s.Preview(sc, r)
	if e != nil {
		t.Fatal(e)
	}
	r.ApprovalID = a.ApprovalID
	r.ExpectedPlanHash = a.PlanHash
	first, e := s.Execute(context.Background(), sc, r, func(context.Context, Approval) (bool, error) { return true, nil })
	if e != nil {
		t.Fatal(e)
	}
	s.mu.Lock()
	x := s.data.Executions[first.ID]
	x.FinishedAt = time.Now().Add(-time.Hour)
	s.data.Executions[first.ID] = x
	for i := 0; i < 999; i++ {
		id := fmt.Sprintf("history-%d", i)
		s.data.Executions[id] = Execution{ID: id, OwnerID: sc.OwnerID, State: "completed", FinishedAt: time.Now()}
	}
	s.mu.Unlock()
	fresh := r
	fresh.ApprovalID = ""
	fresh.ExpectedPlanHash = ""
	if _, e = s.Execute(context.Background(), sc, fresh, func(context.Context, Approval) (bool, error) { return true, nil }); e != nil {
		t.Fatal("history blocked new execution", e)
	}
	if _, e = s.Execute(context.Background(), sc, r, func(context.Context, Approval) (bool, error) { return true, nil }); e == nil {
		t.Fatal("evicted approval replayed")
	}
	if tr.calls != 2 {
		t.Fatal("unexpected repeat dispatch")
	}
	if len(s.Snapshot(sc).Executions) > 1000 {
		t.Fatal("history not bounded")
	}
}
func TestScheduledClaimsCompactWithDurableReplayFloor(t *testing.T) {
	s, sc, r, tr := setupService(t)
	b, e := s.PutRunbook(sc, Runbook{RouteID: r.RouteID, Command: "true", Enabled: true})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.PutGrant(sc, Grant{RouteID: r.RouteID, CallerID: "scheduler", Capability: "runbook", RunbookID: b.ID, MaxUses: 10, Enabled: true, ExpiresAt: time.Now().Add(time.Hour)}); e != nil {
		t.Fatal(e)
	}
	s.mu.Lock()
	for i := int64(1); i <= 10000; i++ {
		key := fmt.Sprintf("%s:job-1-task-%d", sc.OwnerID, i)
		s.data.ScheduledRuns[key] = ScheduledRun{OwnerID: sc.OwnerID, TaskID: i, State: "claimed"}
	}
	s.mu.Unlock()
	in := RunbookRequest{RunbookID: b.ID}
	if _, e = s.ExecuteScheduledRunbook(context.Background(), sc.OwnerID, "job-1-task-10001", in); e != nil {
		t.Fatal("claim retention bricked new firing", e)
	}
	s.mu.Lock()
	floor := s.data.ScheduledTaskFloor
	count := len(s.data.ScheduledRuns)
	s.mu.Unlock()
	if floor != 2000 || count != 8001 {
		t.Fatalf("incorrect compaction floor%d count%d", floor, count)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := NewService(s.path, s.vault, tr)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	if _, e = reopened.ExecuteScheduledRunbook(context.Background(), sc.OwnerID, "job-1-task-1", in); e == nil {
		t.Fatal("pruned firing replayed after restart")
	}
	if _, e = reopened.ExecuteScheduledRunbook(context.Background(), sc.OwnerID, "job-1-task-10002", in); e != nil {
		t.Fatal("new firing blocked", e)
	}
	if tr.calls != 2 {
		t.Fatal("pruned firing dispatched")
	}
}
func TestMalformedScheduledFiringIDsDenied(t *testing.T) {
	s, sc, _, tr := setupService(t)
	for _, firing := range []string{"", "job-1-task--1", "job-1-task-0", "job-1-task-NaN", "job-1-task-9223372036854775808", "job-1-task-01", "task-123"} {
		if _, e := s.ExecuteScheduledRunbook(context.Background(), sc.OwnerID, firing, RunbookRequest{}); e == nil {
			t.Fatalf("malformed firing accepted %q", firing)
		}
	}
	if tr.calls != 0 {
		t.Fatal("invalid IDs dispatched")
	}
}
func TestCompletedPrunedFiringDoesNotResurrectClaim(t *testing.T) {
	s, sc, r, tr := setupService(t)
	b, e := s.PutRunbook(sc, Runbook{RouteID: r.RouteID, Command: "true", Enabled: true})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.PutGrant(sc, Grant{RouteID: r.RouteID, CallerID: "scheduler", Capability: "runbook", RunbookID: b.ID, MaxUses: 5, Enabled: true, ExpiresAt: time.Now().Add(time.Hour)}); e != nil {
		t.Fatal(e)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	tr.run = func(context.Context) (TransportResult, error) {
		close(started)
		<-release
		code := 0
		return TransportResult{Dispatched: true, ExitCode: &code}, nil
	}
	done := make(chan error, 1)
	go func() {
		_, runErr := s.ExecuteScheduledRunbook(context.Background(), sc.OwnerID, "job-1-task-1", RunbookRequest{RunbookID: b.ID})
		done <- runErr
	}()
	<-started
	s.mu.Lock()
	delete(s.data.ScheduledRuns, sc.OwnerID+":job-1-task-1")
	s.data.ScheduledTaskFloor = 1
	if e = s.save(); e != nil {
		t.Fatal(e)
	}
	s.mu.Unlock()
	close(release)
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	s.mu.Lock()
	_, exists := s.data.ScheduledRuns[sc.OwnerID+":job-1-task-1"]
	s.mu.Unlock()
	if exists {
		t.Fatal("completed pruned claim resurrected")
	}
	if _, e = s.ExecuteScheduledRunbook(context.Background(), sc.OwnerID, "job-1-task-1", RunbookRequest{RunbookID: b.ID}); e == nil {
		t.Fatal("pruned in-flight task replayed")
	}
}

package remoteaccess

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Parameter bounds the set of values accepted by a reviewed command template.
type Parameter struct {
	Values []string `json:"values"`
}

// Runbook is a versioned, owner-authored command with finite parameter choices.
type Runbook struct {
	ID         string               `json:"id"`
	OwnerID    string               `json:"owner_id"`
	RouteID    string               `json:"route_id"`
	Label      string               `json:"label"`
	Command    string               `json:"command"`
	Parameters map[string]Parameter `json:"parameters"`
	Revision   int                  `json:"revision"`
	Enabled    bool                 `json:"enabled"`
}

// RunbookRequest contains only validated runbook parameters and an execution budget.
type RunbookRequest struct {
	RunbookID      string            `json:"runbook_id"`
	Parameters     map[string]string `json:"parameters"`
	TimeoutSeconds int               `json:"timeout_seconds"`
}

func cloneRunbook(x Runbook) Runbook {
	y := x
	y.Parameters = make(map[string]Parameter, len(x.Parameters))
	for name, p := range x.Parameters {
		y.Parameters[name] = Parameter{Values: append([]string(nil), p.Values...)}
	}
	return y
}
func validCapability(c string) bool {
	switch c {
	case "exec", "key_install", "key_remove", "runbook", "file_get", "file_put", "file_stat", "background_install", "background_start", "background_status", "background_cancel", "terminal":
		return true
	}
	return false
}
func validFileRoot(p string) bool {
	return strings.HasPrefix(p, "/") && p != "/" && path.Clean(p) == p && !strings.ContainsAny(p, "\x00\r\n")
}
func canonicalRequest(r ExecuteRequest) ExecuteRequest {
	r.ApprovalID = ""
	r.ExpectedPlanHash = ""
	return r
}
func grantMatches(g Grant, r ExecuteRequest, books map[string]Runbook) bool {
	if r.Background != nil && (g.FileRoot == "" || g.FileRoot != r.Background.Root) {
		return false
	}
	if r.File != nil && (g.FileRoot == "" || g.FileRoot != r.File.Root) {
		return false
	}
	if r.RunbookID == "" {
		return g.RunbookID == ""
	}
	b, ok := books[r.RunbookID]
	return ok && b.Enabled && b.Revision == r.RunbookRevision && b.RouteID == r.RouteID && g.RunbookID == b.ID && g.RunbookRevision == b.Revision && g.MaxUses > 0 && g.Uses < g.MaxUses
}

var parameterName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,31}$`)

// PutRunbook replaces the reviewed owner definition and invalidates prior standing grants.
func (s *Service) PutRunbook(sc Scope, b Runbook) (Runbook, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.data.Routes[b.RouteID]
	if !validScope(sc) || !ok || r.OwnerID != sc.OwnerID || b.Command == "" || len(b.Command) > 65536 || strings.ContainsRune(b.Command, 0) || len(b.Parameters) > 16 {
		return b, ErrDenied
	}
	for name, p := range b.Parameters {
		if !parameterName.MatchString(name) || len(p.Values) < 1 || len(p.Values) > 32 || !strings.Contains(b.Command, "{{"+name+"}}") {
			return b, ErrDenied
		}
		for _, v := range p.Values {
			if len(v) > 1024 || strings.ContainsRune(v, 0) || strings.Contains(v, "{{") || strings.Contains(v, "}}") {
				return b, ErrDenied
			}
		}
	}
	if len(b.Parameters) > 0 && !safeParameterizedTemplate(b.Command, b.Parameters) {
		return b, ErrDenied
	}
	stripped := b.Command
	for n := range b.Parameters {
		stripped = strings.ReplaceAll(stripped, "{{"+n+"}}", "")
	}
	if strings.Contains(stripped, "{{") || strings.Contains(stripped, "}}") {
		return b, ErrDenied
	}
	b.Revision = 1
	if b.ID != "" {
		old, ok := s.data.Runbooks[b.ID]
		if !ok || old.OwnerID != sc.OwnerID {
			return b, ErrDenied
		}
		b.Revision = old.Revision + 1
	} else {
		b.ID = newID()
	}
	b.OwnerID = sc.OwnerID
	s.data.Runbooks[b.ID] = cloneRunbook(b)
	s.data.PolicyRevision++
	s.cancelOwner(sc.OwnerID)
	return b, s.save()
}

// ExecuteRunbook permits unattended dispatch only through an exact standing grant.
func (s *Service) ExecuteRunbook(ctx context.Context, sc Scope, in RunbookRequest) (Execution, error) {
	s.mu.Lock()
	b, ok := s.data.Runbooks[in.RunbookID]
	b = cloneRunbook(b)
	s.mu.Unlock()
	if !ok || b.OwnerID != sc.OwnerID || !b.Enabled || len(in.Parameters) != len(b.Parameters) {
		return Execution{}, ErrDenied
	}
	command := b.Command
	for name, p := range b.Parameters {
		v, ok := in.Parameters[name]
		if !ok {
			return Execution{}, ErrDenied
		}
		allowed := false
		for _, candidate := range p.Values {
			if v == candidate {
				allowed = true
			}
		}
		if !allowed {
			return Execution{}, ErrDenied
		}
		quoted := "'" + strings.ReplaceAll(v, "'", "'\"'\"'") + "'"
		command = strings.ReplaceAll(command, "{{"+name+"}}", quoted)
	}
	return s.execute(ctx, sc, ExecuteRequest{RouteID: b.RouteID, Command: command, Capability: "runbook", RunbookID: b.ID, RunbookRevision: b.Revision, TimeoutSeconds: in.TimeoutSeconds}, func(context.Context, Approval) (bool, error) { return true, nil })
}

// ExecuteOperation uses the same consent and durable dispatcher for file and managed job operations.
func (s *Service) ExecuteOperation(ctx context.Context, sc Scope, r ExecuteRequest, c ConfirmFunc) (Execution, error) {
	if err := validateOperation(&r); err != nil {
		return Execution{}, err
	}
	return s.execute(ctx, sc, r, c)
}

// PreviewOperation freezes file or managed job consent using the shared policy.
func (s *Service) PreviewOperation(sc Scope, r ExecuteRequest) (Approval, error) {
	if err := validateOperation(&r); err != nil {
		return Approval{}, err
	}
	return s.Preview(sc, r)
}
func validateOperation(r *ExecuteRequest) error {
	switch r.Operation {
	case "file_get", "file_put", "file_stat":
		if r.File == nil || r.Background != nil || !validFileRoot(r.File.Root) || r.File.Path == "" || path.IsAbs(r.File.Path) || path.Clean(r.File.Path) != r.File.Path || r.File.Path == ".." || strings.HasPrefix(r.File.Path, "../") || strings.ContainsRune(r.File.Path, 0) || len(r.File.Data) > 10<<20 {
			return ErrDenied
		}
		if r.Operation == "file_put" {
			h := sha256.Sum256(r.File.Data)
			if r.File.SHA256 != hex.EncodeToString(h[:]) {
				return ErrDenied
			}
		} else if len(r.File.Data) > 0 {
			return ErrDenied
		}
		r.Command = fmt.Sprintf("%s %s/%s sha256=%s overwrite=%t", r.Operation, r.File.Root, r.File.Path, r.File.SHA256, r.File.Overwrite)
	case "background_install", "background_start", "background_status", "background_cancel":
		if r.Background == nil || r.File != nil || !validFileRoot(r.Background.Root) || !regexp.MustCompile(`^[a-zA-Z0-9_-]{16,80}$`).MatchString(r.Background.JobID) || len(r.Background.Command) > 32768 || strings.ContainsRune(r.Background.Command, 0) {
			return ErrDenied
		}
		r.Command = fmt.Sprintf("%s %s/%s helper_sha256=%s\n%s", r.Operation, r.Background.Root, r.Background.JobID, sha256Hex([]byte(backgroundHelper)), r.Background.Command)
	default:
		return ErrDenied
	}
	r.Capability = r.Operation
	return nil
}

// RemoteJob keeps immutable execution identity even when the remote outcome is unknown.
type RemoteJob struct {
	ID          string `json:"id"`
	OwnerID     string `json:"owner_id"`
	RouteID     string `json:"route_id"`
	Root        string `json:"root"`
	CommandHash string `json:"command_hash"`
	ExecutionID string `json:"execution_id"`
	State       string `json:"state"`
}

func recoveryOperation(r ExecuteRequest) bool {
	return r.Operation == "background_status" || r.Operation == "background_cancel"
}

// ScheduledRun is a durable no-replay claim for one scheduler queue task.
type ScheduledRun struct {
	TaskID      int64  `json:"task_id"`
	OwnerID     string `json:"owner_id"`
	RequestHash string `json:"request_hash"`
	ExecutionID string `json:"execution_id"`
	State       string `json:"state"`
}

// ExecuteScheduledRunbook claims a firing before dispatch. A crash leaves an
// unknown result; re-delivery never repeats the remote operation.
func (s *Service) ExecuteScheduledRunbook(ctx context.Context, owner, firing string, in RunbookRequest) (Execution, error) {
	taskID := scheduledTaskID(firing)
	if owner == "" || taskID <= 0 || len(firing) > 128 {
		return Execution{}, ErrDenied
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return Execution{}, ErrDenied
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	key := owner + ":" + firing
	s.mu.Lock()
	if taskID <= s.data.ScheduledTaskFloor {
		s.mu.Unlock()
		return Execution{}, ErrDenied
	}
	if prior, ok := s.data.ScheduledRuns[key]; ok {
		if prior.OwnerID != owner || prior.RequestHash != hash {
			s.mu.Unlock()
			return Execution{}, ErrDenied
		}
		x, ok := s.data.Executions[prior.ExecutionID]
		s.mu.Unlock()
		if ok {
			return x, nil
		}
		return Execution{State: "unknown_outcome", ErrorCode: "scheduled_firing_already_claimed"}, nil
	}
	if len(s.data.ScheduledRuns) >= 10000 {
		ids := make([]int64, 0, len(s.data.ScheduledRuns))
		for key, record := range s.data.ScheduledRuns {
			id := record.TaskID
			if id == 0 {
				idx := strings.LastIndex(key, ":job-")
				if idx >= 0 {
					id = scheduledTaskID(key[idx+1:])
				}
			}
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		cutoff := ids[len(ids)-8001]
		if cutoff > s.data.ScheduledTaskFloor {
			s.data.ScheduledTaskFloor = cutoff
		}
		for key, record := range s.data.ScheduledRuns {
			id := record.TaskID
			if id == 0 {
				idx := strings.LastIndex(key, ":job-")
				if idx >= 0 {
					id = scheduledTaskID(key[idx+1:])
				}
			}
			if id <= s.data.ScheduledTaskFloor {
				delete(s.data.ScheduledRuns, key)
			}
		}
		if taskID <= s.data.ScheduledTaskFloor {
			err = s.save()
			s.mu.Unlock()
			if err != nil {
				return Execution{}, err
			}
			return Execution{}, ErrDenied
		}
	}
	s.data.ScheduledRuns[key] = ScheduledRun{TaskID: taskID, OwnerID: owner, RequestHash: hash, State: "claimed"}
	err = s.save()
	s.mu.Unlock()
	if err != nil {
		return Execution{}, err
	}
	x, runerr := s.ExecuteRunbook(ctx, Scope{OwnerID: owner, CallerID: "scheduler"}, in)
	s.mu.Lock()
	record, exists := s.data.ScheduledRuns[key]
	if !exists {
		s.mu.Unlock()
		return x, runerr
	}
	record.ExecutionID = x.ID
	record.State = x.State
	if record.State == "" {
		record.State = "denied"
	}
	s.data.ScheduledRuns[key] = record
	saveerr := s.save()
	s.mu.Unlock()
	if saveerr != nil {
		return x, saveerr
	}
	return x, runerr
}

// Parameterized templates deliberately support a single simple command only.
// Shell syntax belongs in fixed parameter-free reviewed runbooks, never around
// substituted values where quoting could acquire a different meaning.
func safeParameterizedTemplate(command string, parameters map[string]Parameter) bool {
	if strings.ContainsAny(command, "\t\r\n") {
		return false
	}
	tokens := strings.Split(command, " ")
	literal := regexp.MustCompile(`^[a-zA-Z0-9_./:@%+=,-]+$`)
	first := true
	for _, token := range tokens {
		if token == "" {
			continue
		}
		if strings.HasPrefix(token, "{{") && strings.HasSuffix(token, "}}") {
			name := strings.TrimSuffix(strings.TrimPrefix(token, "{{"), "}}")
			if _, ok := parameters[name]; !ok || first {
				return false
			}
		} else if !literal.MatchString(token) {
			return false
		}
		first = false
	}
	return !first
}

var scheduledFiringPattern = regexp.MustCompile(`^job-[1-9]\d*-task-([1-9]\d*)$`)

func scheduledTaskID(firing string) int64 {
	parts := scheduledFiringPattern.FindStringSubmatch(firing)
	if len(parts) != 2 {
		return 0
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0
	}
	return id
}

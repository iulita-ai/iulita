// Package engine executes accepted mobile turns without legacy memory, global
// events, generic tool retries, response caches or detached background analysis.
package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/mobile/conversations"
	"github.com/iulita-ai/iulita/internal/skill"
)

// Tool defines one explicitly allowed mobile tool and its execution policy.
type Tool struct {
	Definition       llm.ToolDefinition
	Revision, Effect string
	Isolated         bool
	RequiredScopes   []string
	FreshAuth        bool
	// Execute returns only ordinary chat-safe output. Phone tools may return
	// task IDs/template statuses, never observations or free-form phone summaries.
	Execute func(context.Context, conversations.Fence, json.RawMessage) (string, error)
}

// Profile pins a model binding and revision for accepted execution.
type Profile struct {
	Provider llm.Provider
	Revision uint64
	Digest   string
}

// Resolver resolves an explicitly requested immutable model profile.
type Resolver func(context.Context, string, string) (Profile, error)

// MemorySource provides the explicitly permitted saved-memory view.
type MemorySource interface {
	ReadSavedMemory(context.Context, string) (string, error)
}

// Models uses the same verified profile registry as the existing Assistant,
// but invokes the resolved immutable client directly, without fallback/cache.
func Models(invoker llm.ProfileInvoker) Resolver {
	return func(ctx context.Context, owner, id string) (Profile, error) {
		s := invoker.AcquireProfileSnapshot()
		b, err := s.ResolvePinned(id)
		if err != nil {
			return Profile{}, err
		}
		data, _ := json.Marshal(b.Profile) //nolint:errcheck // This internal snapshot contains only JSON-supported values.
		sum := sha256.Sum256(data)
		return Profile{b.Provider, s.Revision(), hex.EncodeToString(sum[:])}, nil
	}
}

type policy struct {
	Version         int
	ProfileRevision uint64
	ProfileDigest   string
	Tools           map[string]string
	MemoryMode      string
	SavedMemory     string
	Authority       Authority
}

// Engine executes accepted mobile turns under durable fencing and policy.
type Engine struct {
	Repo            *conversations.Repository
	Resolve         Resolver
	System          string
	tools           map[string]Tool
	Timeout         time.Duration
	AccountMemory   MemorySource
	VerifyAuthority func(context.Context, Authority) error
	started         chan struct{}
	startOnce       sync.Once
}

// New constructs the subsystem with explicit dependencies and limits.
func New(repo *conversations.Repository, resolve Resolver, system string, tools []Tool) (*Engine, error) {
	if repo == nil || resolve == nil {
		return nil, errors.New("mobile execution dependencies required")
	}
	e := &Engine{Repo: repo, Resolve: resolve, System: system, Timeout: 2 * time.Minute, tools: map[string]Tool{}, started: make(chan struct{})}
	for _, t := range tools { //nolint:gocritic // Copy each tool before isolating its mutable slices.
		if t.Definition.Name == "" || t.Revision == "" || t.Execute == nil || (t.Effect != "read" && t.Effect != "mutating" && t.Effect != "unknown") {
			return nil, errors.New("invalid explicit mobile tool")
		}
		if _, ok := e.tools[t.Definition.Name]; ok {
			return nil, errors.New("duplicate mobile tool")
		}
		t.RequiredScopes = append([]string(nil), t.RequiredScopes...)
		t.Definition.InputSchema = append([]byte(nil), t.Definition.InputSchema...)
		e.tools[t.Definition.Name] = t
	}
	return e, nil
}

// Ready signals completion of execution-runtime initialization.
func (e *Engine) Ready() <-chan struct{} { return e.started }
func toolDigest(t Tool) string {
	data, _ := json.Marshal(struct { //nolint:errcheck // This internal snapshot contains only JSON-supported values.
		D      llm.ToolDefinition
		R, E   string
		I      bool
		Scopes []string
		Fresh  bool
	}{t.Definition, t.Revision, t.Effect, t.Isolated, t.RequiredScopes, t.FreshAuth})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Accept admits a message with pinned policy and model configuration.
func (e *Engine) Accept(ctx context.Context, owner, conversation string, in conversations.Input) (conversations.Turn, bool, error) {
	return e.Repo.Admit(ctx, owner, conversation, in, func(t conversations.Turn) ([]byte, error) {
		p, err := e.Resolve(ctx, owner, t.AcceptedSettings.ProfileID)
		if err != nil || p.Provider == nil || p.Digest == "" || p.Revision == 0 {
			return nil, errors.New("requested mobile model profile unavailable")
		}
		frozen := policy{Version: 1, ProfileRevision: p.Revision, ProfileDigest: p.Digest, Tools: map[string]string{}, MemoryMode: t.AcceptedSettings.MemoryMode, Authority: AuthorityFrom(ctx)}
		if frozen.Authority.OwnerID != "" && frozen.Authority.OwnerID != owner {
			return nil, conversations.ErrInvalid
		}
		if frozen.MemoryMode == "account" {
			if e.AccountMemory == nil {
				return nil, errors.New("saved account memory unavailable")
			}
			frozen.SavedMemory, err = e.AccountMemory.ReadSavedMemory(ctx, owner)
			if err != nil {
				return nil, err
			}
			if len(frozen.SavedMemory) > 24<<10 {
				return nil, errors.New("saved account memory exceeds limit")
			}
		}
		for name, tool := range e.tools { //nolint:gocritic // Map values are immutable tool snapshots.
			if !permits(frozen.Authority, tool) {
				continue
			}
			if frozen.MemoryMode == "isolated" && !tool.Isolated {
				continue
			}
			frozen.Tools[name] = toolDigest(tool)
		}
		return json.Marshal(frozen)
	})
}

// Run owns a bounded set of business workers rooted in application shutdown,
// never a SendMessage RPC. No running turn is replayed after coordinator loss.
func (e *Engine) Run(ctx context.Context) error {
	rt, err := e.Repo.AcquireRuntime(ctx)
	if err != nil {
		return err
	}
	e.startOnce.Do(func() { close(e.started) })
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_ = e.Repo.ReleaseRuntime(cleanup, rt) //nolint:errcheck // The lease expires if shutdown cleanup cannot reach SQLite.
	}()
	root, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	defer wg.Wait()
	// The database enforces owner caps; the semaphore also bounds global work.
	sem := make(chan struct{}, 16)
	workerError := make(chan error, 1)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	renew := time.NewTicker(3 * time.Second)
	defer renew.Stop()
	for {
		select {
		case stepErr1 := <-workerError:
			cancel()
			return stepErr1
		case <-root.Done():
			return root.Err()
		case <-renew.C:
			if err = e.Repo.RenewRuntime(root, rt); err != nil {
				cancel()
				return err
			}
		case <-ticker.C:
			if err = e.Repo.ReconcileSecurity(root); err != nil {
				cancel()
				return err
			}
			owners, err := e.Repo.PendingOwners(root)
			if err != nil {
				cancel()
				return err
			}
			for _, owner := range owners {
				for i := 0; i < 2; i++ {
					acquired := false
					select {
					case sem <- struct{}{}:
						acquired = true
					default:
					}
					if !acquired {
						break
					}
					t, f, err := e.Repo.Claim(root, rt, owner)
					if err != nil {
						<-sem
						if errors.Is(err, conversations.ErrNoTurn) {
							break
						}
						cancel()
						return err
					}
					wg.Add(1)
					go func() {
						defer wg.Done()
						defer func() { <-sem }()
						if err := e.process(root, t, f); err != nil {
							select {
							case workerError <- err:
							default:
							}
						}
					}()
				}
			}
		}
	}
}
func (e *Engine) process(root context.Context, t conversations.Turn, f conversations.Fence) (finalizationError error) {
	absoluteBudget := time.Now().Add(10 * time.Minute)
	business, cancel := e.Repo.TurnContext(root, f)
	defer cancel()
	ctx, deadline := context.WithTimeout(business, e.Timeout)
	defer deadline()
	ctx = skill.WithDeadlineExtender(ctx, acceptedBudget(business, absoluteBudget))
	state := "failed"
	text := ""
	var authorityVerifier func(context.Context) error
	defer func() {
		cleanup, c := context.WithTimeout(context.WithoutCancel(root), 5*time.Second)
		defer c()
		guarded := cleanup
		if authorityVerifier != nil {
			guarded = conversations.WithVerifier(cleanup, authorityVerifier)
		}
		err := e.Repo.Complete(guarded, f, text, state)
		if errors.Is(err, conversations.ErrFenced) {
			if _, cancelErr := e.Repo.Cancel(cleanup, f.OwnerID, f.ConversationID, f.TurnID, f.Generation); cancelErr != nil && !errors.Is(cancelErr, conversations.ErrFenced) {
				finalizationError = errors.New("mobile cancellation finalization unavailable")
			}
			return
		}
		if err != nil && !errors.Is(err, conversations.ErrFenced) {
			if fallback := e.Repo.Complete(guarded, f, "", "unknown_outcome"); fallback != nil && !errors.Is(fallback, conversations.ErrFenced) {
				finalizationError = errors.New("mobile turn finalization unavailable")
			}
		}
	}()
	defer func() {
		if recover() != nil {
			state = "unknown_outcome"
			text = ""
		}
	}()
	body, err := e.Repo.Policy(ctx, f)
	if err != nil {
		return
	}
	var p policy
	if json.Unmarshal(body, &p) != nil || p.Version != 1 || p.MemoryMode != t.AcceptedSettings.MemoryMode {
		return
	}
	if p.Authority.FamilyID != "" {
		if e.VerifyAuthority == nil {
			return
		}
		authorityVerifier = func(ctx context.Context) error {
			if e.VerifyAuthority(ctx, p.Authority) != nil {
				return conversations.ErrFenced
			}
			return nil
		}
		familyRoot, familyCancel := e.Repo.TurnContext(conversations.WithVerifier(business, authorityVerifier), f)
		defer familyCancel()
		deadline()
		var executionCancel context.CancelFunc
		ctx, executionCancel = context.WithTimeout(familyRoot, e.Timeout)
		defer executionCancel()
		ctx = skill.WithDeadlineExtender(ctx, acceptedBudget(familyRoot, absoluteBudget))
	}
	ctx = WithAuthority(ctx, p.Authority)
	profile, err := e.Resolve(ctx, t.OwnerID, t.AcceptedSettings.ProfileID)
	if err != nil || profile.Revision != p.ProfileRevision || profile.Digest != p.ProfileDigest || profile.Provider == nil {
		return
	}
	history, input, err := e.Repo.ContextFor(ctx, f)
	if err != nil {
		return
	}
	req := llm.Request{SystemPrompt: e.System, Message: input, ProfileID: t.AcceptedSettings.ProfileID, PinnedProfile: true, Operation: "mobile.chat", UserID: t.OwnerID, ChatID: "mobile:" + t.ConversationID, PolicyRevision: p.ProfileRevision}
	if p.MemoryMode == "account" && p.SavedMemory != "" {
		req.SystemPrompt += "\n\nAccount saved knowledge (explicit account memory mode):\n" + p.SavedMemory
	}
	for i := range history {
		m := &history[i]
		req.History = append(req.History, domain.ChatMessage{Role: domain.Role(m.Role), Content: m.Text})
	}
	var names []string
	for name, digest := range p.Tools {
		tool, ok := e.tools[name]
		if !ok || toolDigest(tool) != digest {
			return
		}
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		req.Tools = append(req.Tools, e.tools[name].Definition)
	}
	for round := 0; round < 10; round++ {
		if e.Repo.CheckFence(ctx, f) != nil {
			return
		}
		current, err := e.Resolve(ctx, t.OwnerID, t.AcceptedSettings.ProfileID)
		if err != nil || current.Revision != p.ProfileRevision || current.Digest != p.ProfileDigest {
			return
		}
		response, err := profile.Provider.Complete(ctx, req)
		if err != nil {
			return
		}
		if len(response.ToolCalls) == 0 {
			text = response.Content
			state = "completed"
			return
		}
		if len(response.ToolCalls) > 16 {
			return
		}
		exchange := llm.ToolExchange{AssistantText: response.Content, ToolCalls: response.ToolCalls, ReasoningContent: response.ReasoningContent, ProfileID: req.ProfileID}
		for _, call := range response.ToolCalls {
			tool, ok := e.tools[call.Name]
			if !ok || p.Tools[call.Name] == "" || !permits(p.Authority, tool) {
				exchange.Results = append(exchange.Results, llm.ToolResult{ToolCallID: call.ID, Content: "Tool unavailable in this conversation policy.", IsError: true})
				continue
			}
			if len(call.Input) > 1<<20 || !json.Valid(call.Input) {
				return
			}
			// Canonicalize JSON so changed object key ordering or a new model call
			// ID cannot replay an identical semantic operation in a later round.
			var semantic any
			decoder := json.NewDecoder(strings.NewReader(string(call.Input)))
			decoder.UseNumber()
			if decoder.Decode(&semantic) != nil {
				return
			}
			canonical, _ := json.Marshal(semantic) //nolint:errcheck // This internal snapshot contains only JSON-supported values.
			payload, _ := json.Marshal(struct {    //nolint:errcheck // This internal snapshot contains only JSON-supported values.
				Name  string
				Input json.RawMessage
			}{call.Name, canonical})
			operationHash := sha256.Sum256(payload)
			id := hex.EncodeToString(operationHash[:])
			op, err := e.Repo.PrepareTool(ctx, f, id, tool.Effect, payload)
			if err != nil {
				return
			}
			if op.State == "succeeded" {
				exchange.Results = append(exchange.Results, llm.ToolResult{ToolCallID: call.ID, Content: op.Outcome})
				continue
			}
			if op.State == "failed" {
				exchange.Results = append(exchange.Results, llm.ToolResult{ToolCallID: call.ID, Content: "Operation was rejected before dispatch.", IsError: true})
				continue
			}
			if op.State != "prepared" {
				state = "unknown_outcome"
				return
			}
			if err = e.Repo.StartTool(ctx, f, id); err != nil {
				return
			}
			// Started is committed BEFORE the external call. From this point any
			// ambiguous error is terminal; no model-driven mutation retry follows.
			result, err := tool.Execute(ctx, f, call.Input)
			if err != nil {
				var rejection Rejection
				if errors.As(err, &rejection) {
					if e.Repo.FinishTool(ctx, f, id, "failed", "") != nil {
						return
					}
					exchange.Results = append(exchange.Results, llm.ToolResult{ToolCallID: call.ID, Content: "Operation unavailable or rejected before dispatch.", IsError: true})
					continue
				}
				state = "unknown_outcome"
				_ = e.Repo.FinishTool(ctx, f, id, "unknown_outcome", "") //nolint:errcheck // Complete preserves unknown outcome if this expired context cannot update the journal.
				return
			}
			if len(result) > 64<<10 {
				state = "unknown_outcome"
				return
			}
			if err = e.Repo.FinishTool(ctx, f, id, "succeeded", result); err != nil {
				return
			}
			exchange.Results = append(exchange.Results, llm.ToolResult{ToolCallID: call.ID, Content: result})
		}
		req.ToolExchanges = append(req.ToolExchanges, exchange)
	}
	return
}

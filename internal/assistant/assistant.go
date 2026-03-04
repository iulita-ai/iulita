package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/eventbus"
	"github.com/iulita-ai/iulita/internal/i18n"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/skill/interact"
	"github.com/iulita-ai/iulita/internal/storage"

	"golang.org/x/text/language"
)

const (
	historyLimit  = 50
	maxIterations = 10
)

// Assistant orchestrates the message flow: channel -> storage -> LLM -> storage.
type Assistant struct {
	provider          llm.Provider
	store             storage.Repository
	registry          *skill.Registry
	systemPrompt      string
	defaultTimezone   string
	techAnalyzer      *TechFactAnalyzer
	logger            *zap.Logger
	contextWindow     int
	thinkingBudget    int64         // extended thinking budget in tokens (0 = disabled)
	requestTimeout    time.Duration // per-message timeout (0 = default 120s)
	autoLinkSummary   bool
	maxLinks          int
	streaming         bool
	streamSender      channel.StreamingSender
	statusNotifier    channel.StatusNotifier
	preHooks          []PreprocessorHook
	postHooks         []PostprocessorHook
	bus               *eventbus.Bus
	embedder          llm.EmbeddingProvider
	vectorWeight      float64
	memoryTriggers    []string // lowercased keywords that force remember tool
	lastInputTokens   atomic.Int64
	totalInputTokens  atomic.Int64
	totalOutputTokens atomic.Int64
	totalRequests     atomic.Int64
	bgWg              sync.WaitGroup // tracks fire-and-forget background goroutines
	steerCh           chan InjectedMessage
	followUpCh        chan InjectedMessage
	approvals         *approvalStore
	sender            channel.MessageSender // for sending approval prompts
	prompterFactory   interact.PromptAskerFactory
}

// New creates a new Assistant.
func New(provider llm.Provider, store storage.Repository, registry *skill.Registry, systemPrompt string, defaultTimezone string, contextWindow int, logger *zap.Logger) *Assistant {
	if contextWindow <= 0 {
		contextWindow = defaultContextWindow
	}

	return &Assistant{
		provider:        provider,
		store:           store,
		registry:        registry,
		systemPrompt:    systemPrompt,
		defaultTimezone: defaultTimezone,
		techAnalyzer:    NewTechFactAnalyzer(store, logger),
		contextWindow:   contextWindow,
		logger:          logger,
		steerCh:         make(chan InjectedMessage, steerBufferSize),
		followUpCh:      make(chan InjectedMessage, followUpBufferSize),
		approvals:       newApprovalStore(),
	}
}

// staticSystemPrompt returns the stable portion of the system prompt:
// base instructions + skill system prompts. This content changes only when
// skills are toggled at runtime — not per message — and is eligible for
// provider-side caching (e.g. Anthropic cache_control: ephemeral).
func (a *Assistant) staticSystemPrompt() string {
	var b strings.Builder
	b.WriteString(a.systemPrompt)

	// Skill system prompts from manifests (both internal and text-only skills).
	for _, sp := range a.registry.SystemPrompts() {
		b.WriteString("\n\n")
		b.WriteString(sp)
	}

	return b.String()
}

// dynamicSystemPrompt builds the per-message portion of the system prompt:
// current time, directives, facts, insights, user profile, language directive.
func (a *Assistant) dynamicSystemPrompt(directive, facts, insights, techFacts, currentTime string, localeTag ...language.Tag) string {
	var b strings.Builder

	// Always inject current time so the model never guesses the date.
	if currentTime != "" {
		b.WriteString("## Current Time (IMPORTANT)\n")
		b.WriteString(currentTime)
		b.WriteString("\nYou MUST use this exact date and time as the ground truth. NEVER guess, assume, or infer the current date from conversation history or any other source.")
	}

	if directive != "" {
		b.WriteString("\n\n## User Directives\n")
		b.WriteString(directive)
	}

	if techFacts != "" {
		b.WriteString("\n\n## User Profile\n")
		b.WriteString(techFacts)
	}

	if facts != "" {
		b.WriteString("\n\n## Remembered Facts\n")
		b.WriteString(facts)
	}

	if insights != "" {
		b.WriteString("\n\n## Insights\n")
		b.WriteString(insights)
	}

	// Add language directive when locale is not English.
	if len(localeTag) > 0 && localeTag[0] != language.English {
		langName := i18n.LanguageName(localeTag[0])
		b.WriteString("\n\n## Language\n")
		b.WriteString(i18n.Tl(localeTag[0], "AssistantLanguageDirective", map[string]any{"Language": langName}))
	}

	return b.String()
}

// HandleMessage implements channel.MessageHandler.
func (a *Assistant) HandleMessage(ctx context.Context, msg channel.IncomingMessage) (string, error) {
	// Bound the entire agentic loop so a single message can't run forever.
	timeout := a.effectiveTimeout()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Enrich context with caller identity for skills.
	ctx = skill.WithChatID(ctx, msg.ChatID)
	// Use resolved iulita user ID if available, otherwise fall back to platform user ID.
	effectiveUserID := msg.ResolvedUserID
	if effectiveUserID == "" {
		effectiveUserID = msg.UserID
	}
	ctx = skill.WithUserID(ctx, effectiveUserID)

	// Inject user role for admin-gated skills.
	if effectiveUserID != "" {
		if u, err := a.store.GetUser(ctx, effectiveUserID); err != nil {
			a.logger.Warn("failed to load user role", zap.String("user_id", effectiveUserID), zap.Error(err))
		} else if u != nil {
			ctx = skill.WithUserRole(ctx, string(u.Role))
		}
	}

	// Inject channel capabilities into context for skill output adaptation.
	if msg.Caps != 0 {
		ctx = channel.WithCaps(ctx, msg.Caps)
	}

	// Resolve and inject locale into context.
	localeTag := i18n.ResolveLocale(msg.Locale, msg.LanguageCode)
	ctx = i18n.WithLocale(ctx, localeTag)

	// Check for pending approval before normal processing.
	if pending, ok := a.approvals.take(msg.ChatID); ok {
		approved, defined := isApprovalResponse(msg.Text, localeTag)
		if defined {
			if approved && a.canApprove(ctx, pending.level) {
				result := a.executeSkill(ctx, msg.ChatID, pending.tc)
				response := i18n.T(ctx, "ApprovalExecuted", map[string]any{"Tool": pending.tc.Name, "Result": result.Content})
				a.saveAssistantResponse(ctx, msg.ChatID, response)
				return response, nil
			}
			cancelled := i18n.T(ctx, "ApprovalCancelled")
			a.saveAssistantResponse(ctx, msg.ChatID, cancelled)
			return cancelled, nil
		}
		// Not an approval reply — discard pending and proceed normally.
	}

	// Inject document attachments into context so skills can access uploaded files.
	if len(msg.Documents) > 0 {
		var skillDocs []skill.DocumentAttachment
		for _, doc := range msg.Documents {
			skillDocs = append(skillDocs, skill.DocumentAttachment{
				Data:     doc.Data,
				MimeType: doc.MimeType,
				Filename: doc.Filename,
			})
		}
		ctx = skill.WithDocuments(ctx, skillDocs)
	}

	// Save the incoming user message (images/documents not stored, only text placeholder).
	content := msg.Text
	if content == "" && len(msg.Images) > 0 {
		content = "[image]"
	}
	for _, doc := range msg.Documents {
		content += fmt.Sprintf(" [document: %s]", doc.Filename)
	}
	content = strings.TrimSpace(content)
	userMsg := &domain.ChatMessage{
		ChatID:    msg.ChatID,
		UserID:    effectiveUserID,
		Role:      domain.RoleUser,
		Content:   content,
		CreatedAt: time.Now(),
	}
	if err := a.store.SaveMessage(ctx, userMsg); err != nil {
		a.logger.Error("failed to save user message", zap.Error(err))
		return "", err
	}

	// Notify client that processing has started (immediate feedback).
	a.notifyStatus(ctx, msg.ChatID, channel.StatusEvent{Type: "processing"})

	// Publish message received event (async subscribers handle side effects).
	if a.bus != nil {
		a.bus.Publish(ctx, eventbus.Event{
			Type: eventbus.MessageReceived,
			Payload: eventbus.MessageReceivedPayload{
				ChatID:   msg.ChatID,
				UserID:   msg.UserID,
				Text:     msg.Text,
				Language: msg.LanguageCode,
				Time:     time.Now(),
			},
		})
	}

	// Background lightweight analysis for tech facts (tracked for graceful shutdown).
	a.bgWg.Add(1)
	go func() {
		defer a.bgWg.Done()
		a.techAnalyzer.AnalyzeMessage(context.WithoutCancel(ctx), msg.ChatID, effectiveUserID, msg.Text)
	}()

	// Fetch conversation history.
	history, err := a.store.GetHistory(ctx, msg.ChatID, historyLimit)
	if err != nil {
		a.logger.Error("failed to get history", zap.Error(err))
		return "", err
	}

	// Compress history if context is getting too large.
	if lit := a.lastInputTokens.Load(); lit > 0 {
		history, err = a.compressIfNeeded(ctx, msg.ChatID, history, lit)
		if err != nil {
			a.logger.Error("compression failed", zap.Error(err))
		}
	}

	// Exclude the last message since we pass it separately.
	var historyForLLM []domain.ChatMessage
	if len(history) > 1 {
		historyForLLM = history[:len(history)-1]
	}

	// Build tool definitions from enabled skills.
	var tools []llm.ToolDefinition
	enabledSkills := a.registry.EnabledSkills()
	for _, s := range enabledSkills {
		schema := s.InputSchema()
		if schema == nil {
			continue // text-only skills have no tool schema
		}
		tools = append(tools, llm.ToolDefinition{
			Name:        s.Name(),
			Description: s.Description(),
			InputSchema: schema,
		})
	}
	if len(tools) > 0 {
		var toolNames []string
		for _, td := range tools {
			toolNames = append(toolNames, td.Name)
		}
		a.logger.Info("tools for LLM", zap.Strings("tools", toolNames), zap.Int("count", len(toolNames)))
	}

	// Convert channel images to LLM images.
	var images []llm.ImageAttachment
	for _, img := range msg.Images {
		images = append(images, llm.ImageAttachment{Data: img.Data, MediaType: img.MediaType})
	}

	// Convert channel documents to LLM documents.
	var documents []llm.DocumentAttachment
	for _, doc := range msg.Documents {
		documents = append(documents, llm.DocumentAttachment{Data: doc.Data, MimeType: doc.MimeType, Filename: doc.Filename})
	}

	// Load directive — prefer user-scoped, fall back to chat-scoped.
	var directiveText string
	if effectiveUserID != "" {
		// User is resolved — use user-scoped directive.
		if d, err := a.store.GetDirectiveByUser(ctx, effectiveUserID); err != nil {
			a.logger.Error("failed to load directive", zap.Error(err))
		} else if d != nil {
			directiveText = d.Content
		}
	}
	if directiveText == "" {
		if d, err := a.store.GetDirective(ctx, msg.ChatID); err != nil {
			a.logger.Error("failed to load directive", zap.Error(err))
		} else if d != nil {
			directiveText = d.Content
		}
	}

	// Load recent facts — prefer user-scoped, fall back to chat-scoped.
	var factsText string
	var recentFacts []domain.Fact
	if effectiveUserID != "" {
		recentFacts, _ = a.store.GetRecentFactsByUser(ctx, effectiveUserID, 20)
	}
	if len(recentFacts) == 0 {
		recentFacts, _ = a.store.GetRecentFacts(ctx, msg.ChatID, 20)
	}
	if len(recentFacts) > 0 {
		var fb strings.Builder
		for _, f := range recentFacts {
			fmt.Fprintf(&fb, "- %s\n", f.Content)
		}
		factsText = fb.String()
	}

	// Load contextually relevant insights — prefer user-scoped.
	var insightsText string
	var insights []domain.Insight
	useUserScope := effectiveUserID != ""
	if query := sanitizeFTSQuery(msg.Text); query != "" {
		if a.embedder != nil && a.vectorWeight > 0 {
			if queryVec, err := a.embedder.Embed(ctx, []string{msg.Text}); err == nil && len(queryVec) > 0 {
				if useUserScope {
					insights, _ = a.store.SearchInsightsHybridByUser(ctx, effectiveUserID, query, queryVec[0], 5, a.vectorWeight)
				} else {
					insights, _ = a.store.SearchInsightsHybrid(ctx, msg.ChatID, query, queryVec[0], 5, a.vectorWeight)
				}
			} else {
				if useUserScope {
					insights, _ = a.store.SearchInsightsByUser(ctx, effectiveUserID, query, 5)
				} else {
					insights, _ = a.store.SearchInsights(ctx, msg.ChatID, query, 5)
				}
			}
		} else {
			if useUserScope {
				insights, _ = a.store.SearchInsightsByUser(ctx, effectiveUserID, query, 5)
			} else {
				insights, _ = a.store.SearchInsights(ctx, msg.ChatID, query, 5)
			}
		}
	}
	if len(insights) == 0 {
		if useUserScope {
			insights, _ = a.store.GetRecentInsightsByUser(ctx, effectiveUserID, 5)
		} else {
			insights, _ = a.store.GetRecentInsights(ctx, msg.ChatID, 5)
		}
	}
	if len(insights) > 0 {
		var ib strings.Builder
		for _, d := range insights {
			fmt.Fprintf(&ib, "- %s\n", d.Content)
			// Background access reinforcement (tracked for graceful shutdown).
			a.bgWg.Add(1)
			go func(id int64) {
				defer a.bgWg.Done()
				if err := a.store.ReinforceInsight(context.Background(), id); err != nil {
					a.logger.Debug("failed to reinforce insight", zap.Int64("id", id), zap.Error(err))
				}
			}(d.ID)
		}
		insightsText = ib.String()
	}

	// Load tech facts for user profile context — prefer user-scoped.
	var techFactsText string
	var tfs []domain.TechFact
	if useUserScope {
		tfs, _ = a.store.GetTechFactsByUser(ctx, effectiveUserID)
	}
	if len(tfs) == 0 {
		tfs, _ = a.store.GetTechFacts(ctx, msg.ChatID)
	}
	if len(tfs) > 0 {
		var tfb strings.Builder
		currentCategory := ""
		for _, tf := range tfs {
			if tf.Category != currentCategory {
				if currentCategory != "" {
					tfb.WriteString("\n")
				}
				fmt.Fprintf(&tfb, "**%s**:\n", tf.Category)
				currentCategory = tf.Category
			}
			fmt.Fprintf(&tfb, "- %s: %s\n", tf.Key, tf.Value)
		}
		techFactsText = tfb.String()
	}

	// Resolve current time in the user's timezone.
	currentTime := a.resolveCurrentTime(ctx, msg.ChatID, effectiveUserID)
	a.logger.Debug("injected current time", zap.String("current_time", currentTime), zap.String("chat_id", msg.ChatID))

	// Run preprocessor hooks.
	a.runPreHooks(ctx, &msg)

	// Enrich message with link summaries if enabled.
	messageText := msg.Text
	if a.autoLinkSummary && messageText != "" {
		messageText = enrichWithLinks(messageText, a.maxLinks)
	}

	// Create exploration ledger for this request.
	ledger := NewExplorationLedger()

	staticPrompt := a.staticSystemPrompt()
	dynamicPrompt := a.dynamicSystemPrompt(directiveText, factsText, insightsText, techFactsText, currentTime, localeTag)

	req := llm.Request{
		StaticSystemPrompt: staticPrompt,
		SystemPrompt:       dynamicPrompt,
		History:            historyForLLM,
		Message:            messageText,
		Images:             images,
		Documents:          documents,
		Tools:              tools,
		ThinkingBudget:     a.thinkingBudget,
	}

	// Force the remember tool when message matches memory trigger keywords.
	if a.matchesMemoryTrigger(messageText) && len(tools) > 0 {
		req.ForceTool = "remember"
		a.logger.Info("memory trigger detected, forcing remember tool",
			zap.String("chat_id", msg.ChatID))
	}

	// Force external proxy tools when message matches their trigger keywords.
	if req.ForceTool == "" && len(tools) > 0 {
		if forceTool := a.registry.MatchForceTool(messageText); forceTool != "" {
			req.ForceTool = forceTool
			a.logger.Info("force tool trigger matched",
				zap.String("tool", forceTool),
				zap.String("chat_id", msg.ChatID))
		}
	}

	// Agentic loop: call LLM, execute tools, repeat until text response.
	var lastResp llm.Response
	compressedThisTurn := false
	for i := 0; i < maxIterations; i++ {
		// ForceTool only applies to the first iteration.
		if i > 0 {
			req.ForceTool = ""
		}
		// Inject ledger summary into dynamic system prompt for subsequent iterations.
		if summary := ledger.Summary(); summary != "" {
			req.SystemPrompt = dynamicPrompt + "\n\n## Exploration Ledger\n" + summary
		}

		// Use streaming for the final call when no tools are expected.
		useStreaming := a.streaming && a.streamSender != nil && len(req.Tools) == 0
		if sp, ok := a.provider.(llm.StreamingProvider); ok && useStreaming {
			a.notifyStatus(ctx, msg.ChatID, channel.StatusEvent{Type: "stream_start"})
			editFn, doneFn, streamErr := a.streamSender.StartStream(ctx, msg.ChatID, msg.MessageID)
			if streamErr == nil {
				var accumulated strings.Builder
				lastResp, err = sp.CompleteStream(ctx, req, func(chunk string) {
					accumulated.WriteString(chunk)
					if accumulated.Len() >= 30 {
						editFn(accumulated.String())
					}
				})
				if err == nil {
					doneFn(lastResp.Content)
				}
			} else {
				lastResp, err = a.provider.Complete(ctx, req)
			}
		} else {
			lastResp, err = a.provider.Complete(ctx, req)
		}
		if err != nil {
			if llm.IsContextTooLarge(err) && !compressedThisTurn {
				a.logger.Warn("context overflow, compressing and retrying",
					zap.String("chat_id", msg.ChatID), zap.Int("iteration", i))
				history = a.forceCompressRequest(ctx, msg.ChatID, &req, history)
				compressedThisTurn = true
				i-- // retry same iteration
				continue
			}
			a.logger.Error("LLM completion failed", zap.Error(err), zap.Int("iteration", i))
			return "", err
		}
		compressedThisTurn = false

		a.lastInputTokens.Store(lastResp.Usage.InputTokens)
		a.totalInputTokens.Add(lastResp.Usage.InputTokens)
		a.totalOutputTokens.Add(lastResp.Usage.OutputTokens)
		a.totalRequests.Add(1)

		a.logger.Info("LLM usage",
			zap.Int64("input_tokens", lastResp.Usage.InputTokens),
			zap.Int64("output_tokens", lastResp.Usage.OutputTokens),
			zap.Int64("cache_read", lastResp.Usage.CacheReadInputTokens),
			zap.Int64("cache_create", lastResp.Usage.CacheCreationInputTokens),
			zap.Int("iteration", i),
		)

		if a.bus != nil {
			a.bus.Publish(ctx, eventbus.Event{
				Type: eventbus.LLMUsage,
				Payload: eventbus.LLMUsagePayload{
					ChatID:                   msg.ChatID,
					InputTokens:              lastResp.Usage.InputTokens,
					OutputTokens:             lastResp.Usage.OutputTokens,
					CacheReadInputTokens:     lastResp.Usage.CacheReadInputTokens,
					CacheCreationInputTokens: lastResp.Usage.CacheCreationInputTokens,
					Iteration:                i,
				},
			})
		}

		// No tool calls — we have the final text response.
		if len(lastResp.ToolCalls) == 0 {
			a.logger.Info("LLM responded without tool calls",
				zap.Int("iteration", i),
				zap.Int("available_tools", len(req.Tools)),
				zap.Int("response_len", len(lastResp.Content)),
			)
			response := lastResp.Content
			a.runPostHooks(ctx, &response)
			a.saveAssistantResponse(ctx, msg.ChatID, response)

			if a.bus != nil {
				a.bus.Publish(ctx, eventbus.Event{
					Type: eventbus.ResponseSent,
					Payload: eventbus.ResponseSentPayload{
						ChatID:   msg.ChatID,
						Response: response,
						Time:     time.Now(),
					},
				})
			}

			return response, nil
		}

		// Execute each tool call.
		exchange := llm.ToolExchange{
			AssistantText: lastResp.Content,
			ToolCalls:     lastResp.ToolCalls,
		}

		for _, tc := range lastResp.ToolCalls {
			// Check for duplicate tool calls via ledger.
			if cachedResult, isDup := ledger.IsDuplicate(tc); isDup {
				a.logger.Info("duplicate tool call, returning cached result",
					zap.String("skill", tc.Name))
				exchange.Results = append(exchange.Results, llm.ToolResult{
					ToolCallID: tc.ID,
					Content:    cachedResult,
					IsError:    false,
				})
				continue
			}

			result := a.executeSkill(ctx, msg.ChatID, tc)
			ledger.Record(tc, result)
			exchange.Results = append(exchange.Results, result)
		}

		req.ToolExchanges = append(req.ToolExchanges, exchange)
	}

	// Exhausted iterations — save any partial text so history stays consistent.
	a.logger.Warn("agentic loop hit max iterations", zap.Int("max", maxIterations))
	if lastResp.Content != "" {
		a.saveAssistantResponse(ctx, msg.ChatID, lastResp.Content)
	}
	return "", fmt.Errorf("tool use loop exceeded %d iterations", maxIterations)
}

func (a *Assistant) executeSkill(ctx context.Context, chatID string, tc llm.ToolCall) llm.ToolResult {
	s, ok := a.registry.Get(tc.Name)
	if !ok {
		a.logger.Warn("unknown or disabled skill requested", zap.String("skill", tc.Name))
		return llm.ToolResult{
			ToolCallID: tc.ID,
			Content:    fmt.Sprintf("error: unknown skill %q", tc.Name),
			IsError:    true,
		}
	}

	// Check approval level before executing.
	level := a.registry.ApprovalLevelFor(tc.Name)
	if level != skill.ApprovalAuto {
		userRole := skill.UserRoleFrom(ctx)
		needsApproval := level == skill.ApprovalPrompt ||
			(level == skill.ApprovalManual && userRole != string(domain.RoleAdmin))
		if needsApproval {
			a.approvals.set(chatID, tc, level)
			prompt := a.buildApprovalPrompt(ctx, tc, level)
			if a.sender != nil {
				_ = a.sender.SendMessage(ctx, chatID, prompt)
			}
			return llm.ToolResult{
				ToolCallID: tc.ID,
				Content:    i18n.T(ctx, "ApprovalAwaiting", map[string]any{"Tool": tc.Name}),
				IsError:    false,
			}
		}
	}

	a.logger.Info("executing skill", zap.String("skill", tc.Name), zap.String("tool_call_id", tc.ID))

	// Inject a PromptAsker into context for interactive skills.
	if a.prompterFactory != nil {
		if prompter := a.prompterFactory.PrompterFor(chatID); prompter != nil {
			ctx = interact.WithPrompter(ctx, prompter)
		}
	}

	// Notify client that a skill is starting.
	a.notifyStatus(ctx, chatID, channel.StatusEvent{Type: "skill_start", SkillName: tc.Name})

	start := time.Now()
	output, err := s.Execute(ctx, tc.Input)
	durationMs := time.Since(start).Milliseconds()

	// Notify client that the skill completed.
	a.notifyStatus(ctx, chatID, channel.StatusEvent{
		Type:       "skill_done",
		SkillName:  tc.Name,
		Success:    err == nil,
		DurationMs: durationMs,
	})

	if a.bus != nil {
		a.bus.Publish(ctx, eventbus.Event{
			Type: eventbus.SkillExecuted,
			Payload: eventbus.SkillExecutedPayload{
				ChatID:     chatID,
				SkillName:  tc.Name,
				ToolCallID: tc.ID,
				Success:    err == nil,
				DurationMs: durationMs,
			},
		})
	}

	if err != nil {
		a.logger.Error("skill execution failed", zap.String("skill", tc.Name), zap.Error(err))
		return llm.ToolResult{
			ToolCallID: tc.ID,
			Content:    fmt.Sprintf("error: %v", err),
			IsError:    true,
		}
	}

	// Log tool result preview for debugging.
	preview := output
	if len(preview) > 500 {
		preview = preview[:500] + "..."
	}
	a.logger.Info("skill execution result",
		zap.String("skill", tc.Name),
		zap.Int("result_len", len(output)),
		zap.Int64("duration_ms", durationMs),
		zap.String("preview", preview))

	// Notify frontend of locale change so it can switch UI language.
	if tc.Name == "set_language" && !strings.Contains(output, "Unknown") {
		// Parse locale from the skill's input parameters using the same resolution.
		var params struct {
			Language string `json:"language"`
		}
		if json.Unmarshal(tc.Input, &params) == nil && params.Language != "" {
			// Try BCP-47 tag first, then language name lookup.
			resolved := i18n.ResolveLocale(params.Language, "")
			if i18n.TagString(resolved) == "en" && !strings.EqualFold(params.Language, "en") && !strings.EqualFold(params.Language, "english") {
				// ResolveLocale couldn't match — try by language name.
				for _, tag := range i18n.SupportedLanguages {
					if strings.EqualFold(i18n.LanguageName(tag), strings.TrimSpace(params.Language)) {
						resolved = tag
						break
					}
				}
			}
			a.notifyStatus(ctx, chatID, channel.StatusEvent{
				Type: "locale_changed",
				Data: map[string]string{"locale": i18n.TagString(resolved)},
			})
		}
	}

	return llm.ToolResult{
		ToolCallID: tc.ID,
		Content:    output,
		IsError:    false,
	}
}

// stopWords contains common stop words in English and Russian that add no search value.
var stopWords = map[string]struct{}{
	// English
	"a": {}, "an": {}, "the": {}, "is": {}, "it": {}, "in": {}, "on": {}, "at": {},
	"to": {}, "for": {}, "of": {}, "and": {}, "or": {}, "but": {}, "not": {},
	"with": {}, "from": {}, "by": {}, "as": {}, "be": {}, "was": {}, "were": {},
	"are": {}, "been": {}, "being": {}, "have": {}, "has": {}, "had": {}, "do": {},
	"does": {}, "did": {}, "will": {}, "would": {}, "could": {}, "should": {},
	"can": {}, "may": {}, "this": {}, "that": {}, "these": {}, "those": {},
	"i": {}, "you": {}, "he": {}, "she": {}, "we": {}, "they": {}, "me": {},
	"my": {}, "your": {}, "his": {}, "her": {}, "our": {}, "their": {},
	"what": {}, "which": {}, "who": {}, "when": {}, "where": {}, "how": {},
	"if": {}, "then": {}, "so": {}, "no": {}, "yes": {},
	// Russian
	"и": {}, "в": {}, "на": {}, "с": {}, "что": {}, "как": {}, "это": {},
	"по": {}, "но": {}, "не": {}, "из": {}, "к": {}, "за": {}, "от": {},
	"до": {}, "у": {}, "о": {}, "же": {}, "бы": {}, "ли": {}, "да": {},
	"нет": {}, "я": {}, "ты": {}, "он": {}, "она": {}, "мы": {}, "вы": {},
	"они": {}, "мне": {}, "мой": {}, "для": {}, "все": {}, "уже": {},
	"так": {}, "тоже": {}, "только": {}, "ещё": {}, "еще": {},
}

// sanitizeFTSQuery strips FTS5 special operators and stop words from user text.
func sanitizeFTSQuery(text string) string {
	// Remove FTS5 operators and special chars.
	replacer := strings.NewReplacer(
		"*", "", "\"", "", "(", "", ")", "",
		"{", "", "}", "", ":", "", "^", "",
		"AND", "", "OR", "", "NOT", "", "NEAR", "",
	)
	q := replacer.Replace(text)

	// Filter stop words and keep meaningful keywords.
	allWords := strings.Fields(strings.TrimSpace(q))
	var words []string
	for _, w := range allWords {
		lower := strings.ToLower(w)
		if len(lower) < 2 {
			continue
		}
		if _, isStop := stopWords[lower]; isStop {
			continue
		}
		words = append(words, w)
	}

	if len(words) > 5 {
		words = words[:5]
	}
	return strings.Join(words, " ")
}

// resolveCurrentTime returns a human-readable timestamp in the user's timezone.
// It checks tech_facts for a stored timezone, falls back to the configured default, then UTC.
func (a *Assistant) resolveCurrentTime(ctx context.Context, chatID, userID string) string {
	tz := a.defaultTimezone

	// Try user model timezone first (for resolved users).
	if userID != "" {
		if u, err := a.store.GetUser(ctx, userID); err == nil && u != nil && u.Timezone != "" && u.Timezone != "UTC" {
			tz = u.Timezone
		}
	}

	// Try to find a user-specific timezone from tech facts.
	if tz == a.defaultTimezone {
		var techFacts []domain.TechFact
		if userID != "" {
			techFacts, _ = a.store.GetTechFactsByCategoryAndUser(ctx, userID, "preferences")
		}
		if len(techFacts) == 0 {
			techFacts, _ = a.store.GetTechFactsByCategory(ctx, chatID, "preferences")
		}
		for _, tf := range techFacts {
			if tf.Key == "timezone" && tf.Value != "" {
				tz = tf.Value
				break
			}
		}
	}

	loc := time.UTC
	if tz != "" {
		if parsed, err := time.LoadLocation(tz); err == nil {
			loc = parsed
		} else {
			a.logger.Warn("invalid timezone, falling back to UTC", zap.String("timezone", tz), zap.Error(err))
		}
	}

	now := time.Now().In(loc)
	zoneName, offset := now.Zone()
	sign := "+"
	if offset < 0 {
		sign = "-"
		offset = -offset
	}
	hours := offset / 3600
	mins := (offset % 3600) / 60

	return fmt.Sprintf("%s %s %s (%s, UTC%s%d:%02d)",
		now.Format("2006-01-02"), now.Format("Monday"), now.Format("15:04:05"),
		zoneName, sign, hours, mins)
}

// SetThinkingBudget configures the extended thinking budget in tokens.
func (a *Assistant) SetThinkingBudget(budget int64) {
	a.thinkingBudget = budget
}

// SetRequestTimeout configures the per-message timeout.
func (a *Assistant) SetRequestTimeout(d time.Duration) {
	a.requestTimeout = d
}

// effectiveTimeout returns the timeout for a single message, accounting for
// thinking budget and tool use iterations. With extended thinking enabled,
// each LLM call can take much longer.
func (a *Assistant) effectiveTimeout() time.Duration {
	base := a.requestTimeout
	if base <= 0 {
		base = 120 * time.Second
	}
	if a.thinkingBudget > 0 {
		// Thinking adds significant latency: ~1s per 100 thinking tokens is a
		// rough estimate. Multiply by maxIterations since each iteration could think.
		thinkingExtra := time.Duration(a.thinkingBudget/100) * time.Second
		if thinkingExtra < 60*time.Second {
			thinkingExtra = 60 * time.Second
		}
		base += thinkingExtra
	}
	return base
}

// SetLinkEnrichment enables auto-fetching and summarizing URLs in messages.
func (a *Assistant) SetLinkEnrichment(maxLinks int) {
	a.autoLinkSummary = true
	a.maxLinks = maxLinks
}

// SetStreaming enables streaming responses with incremental Telegram message editing.
func (a *Assistant) SetStreaming(sender channel.StreamingSender) {
	a.streaming = true
	a.streamSender = sender
}

// SetStatusNotifier attaches a notifier for real-time processing status updates.
func (a *Assistant) SetStatusNotifier(n channel.StatusNotifier) {
	a.statusNotifier = n
}

// notifyStatus sends a status event to the client if a notifier is configured.
func (a *Assistant) notifyStatus(ctx context.Context, chatID string, event channel.StatusEvent) {
	if a.statusNotifier != nil {
		if err := a.statusNotifier.NotifyStatus(ctx, chatID, event); err != nil {
			a.logger.Debug("failed to send status notification", zap.Error(err))
		}
	}
}

// SetEventBus attaches an event bus for publishing lifecycle events.
func (a *Assistant) SetEventBus(bus *eventbus.Bus) {
	a.bus = bus
}

// SetMemoryTriggers configures keywords that force the remember tool.
// Keywords are matched case-insensitively against user messages.
func (a *Assistant) SetMemoryTriggers(triggers []string) {
	a.memoryTriggers = make([]string, len(triggers))
	for i, t := range triggers {
		a.memoryTriggers[i] = strings.ToLower(t)
	}
}

// matchesMemoryTrigger checks if the message text contains any memory trigger keyword.
func (a *Assistant) matchesMemoryTrigger(text string) bool {
	if len(a.memoryTriggers) == 0 {
		return false
	}
	lower := strings.ToLower(text)
	for _, trigger := range a.memoryTriggers {
		if strings.Contains(lower, trigger) {
			return true
		}
	}
	return false
}

// SetEmbedding configures the embedding provider for hybrid search.
func (a *Assistant) SetEmbedding(embedder llm.EmbeddingProvider, vectorWeight float64) {
	a.embedder = embedder
	a.vectorWeight = vectorWeight
}

// AddPreHook registers a preprocessor hook.
func (a *Assistant) AddPreHook(h PreprocessorHook) {
	a.preHooks = append(a.preHooks, h)
}

// AddPostHook registers a postprocessor hook.
func (a *Assistant) AddPostHook(h PostprocessorHook) {
	a.postHooks = append(a.postHooks, h)
}

// SetMessageSender attaches a sender for approval confirmation prompts.
func (a *Assistant) SetMessageSender(s channel.MessageSender) {
	a.sender = s
}

// SetPrompterFactory attaches a factory for creating interactive prompt askers.
// This enables skills to ask users questions during execution.
func (a *Assistant) SetPrompterFactory(f interact.PromptAskerFactory) {
	a.prompterFactory = f
}

// Steer injects a high-priority message into the assistant's processing queue.
// It is processed before the next follow-up message. Non-blocking: if the
// queue is full the message is dropped with a warning log.
// Use for: admin commands, urgent corrections, cancellation signals.
func (a *Assistant) Steer(msg channel.IncomingMessage, cb func(string, error)) {
	im := InjectedMessage{IncomingMessage: msg, Priority: PrioritySteer, ResponseFn: cb}
	select {
	case a.steerCh <- im:
	default:
		a.logger.Warn("steer queue full, dropping message", zap.String("chat_id", msg.ChatID))
	}
}

// FollowUp injects a low-priority message processed when the assistant is idle.
// Non-blocking: if the queue is full the message is dropped with a warning log.
// Use for: cron triggers, heartbeat prompts, non-urgent notifications.
func (a *Assistant) FollowUp(msg channel.IncomingMessage, cb func(string, error)) {
	im := InjectedMessage{IncomingMessage: msg, Priority: PriorityFollowUp, ResponseFn: cb}
	select {
	case a.followUpCh <- im:
	default:
		a.logger.Warn("followUp queue full, dropping message", zap.String("chat_id", msg.ChatID))
	}
}

// Run starts the priority message dispatch loop. It processes steer messages
// before follow-up messages. Should be started in a goroutine: go ast.Run(ctx).
// The loop exits when ctx is cancelled and drains remaining steer messages.
func (a *Assistant) Run(ctx context.Context) {
	a.logger.Info("assistant queue loop started")
	for {
		// Drain steer first (high priority).
		select {
		case <-ctx.Done():
			a.drainSteer()
			a.logger.Info("assistant queue loop stopped")
			return
		case m := <-a.steerCh:
			a.processInjected(ctx, m)
			continue // check steer again before falling to combined select
		default:
		}

		// Combined select: steer still has priority, but we also accept followUp.
		select {
		case <-ctx.Done():
			a.drainSteer()
			a.logger.Info("assistant queue loop stopped")
			return
		case m := <-a.steerCh:
			a.processInjected(ctx, m)
		case m := <-a.followUpCh:
			a.processInjected(ctx, m)
		}
	}
}

// processInjected handles a single injected message and calls the response callback.
func (a *Assistant) processInjected(ctx context.Context, m InjectedMessage) {
	resp, err := a.HandleMessage(ctx, m.IncomingMessage)
	if m.ResponseFn != nil {
		m.ResponseFn(resp, err)
	}
}

// drainSteer processes remaining steer messages after context cancellation.
// It uses a per-message timeout to prevent unbounded drain time.
func (a *Assistant) drainSteer() {
	for {
		select {
		case m := <-a.steerCh:
			// Use background context with timeout since the original is cancelled.
			drainCtx, drainCancel := context.WithTimeout(context.Background(), 30*time.Second)
			resp, err := a.HandleMessage(drainCtx, m.IncomingMessage)
			drainCancel()
			if m.ResponseFn != nil {
				m.ResponseFn(resp, err)
			}
		default:
			return
		}
	}
}

// canApprove checks whether the current user has sufficient privileges for the approval level.
func (a *Assistant) canApprove(ctx context.Context, level skill.ApprovalLevel) bool {
	if level == skill.ApprovalManual {
		return skill.UserRoleFrom(ctx) == string(domain.RoleAdmin)
	}
	return true // ApprovalPrompt: any user can confirm
}

// buildApprovalPrompt creates a human-readable confirmation message for tool execution.
func (a *Assistant) buildApprovalPrompt(ctx context.Context, tc llm.ToolCall, level skill.ApprovalLevel) string {
	msgID := "ApprovalPrompt"
	if level == skill.ApprovalManual {
		msgID = "ApprovalAdminPrompt"
	}
	return i18n.T(ctx, msgID, map[string]any{"Tool": tc.Name, "Params": string(tc.Input)})
}

// Shutdown waits for all background goroutines (tech analysis, insight reinforcement) to complete.
func (a *Assistant) Shutdown() {
	a.bgWg.Wait()
}

// SessionStats returns cumulative token usage for the session.
func (a *Assistant) SessionStats() (inputTokens, outputTokens, requests int64) {
	return a.totalInputTokens.Load(), a.totalOutputTokens.Load(), a.totalRequests.Load()
}

func (a *Assistant) saveAssistantResponse(ctx context.Context, chatID string, content string) {
	userID := skill.UserIDFrom(ctx)
	assistantMsg := &domain.ChatMessage{
		ChatID:    chatID,
		UserID:    userID,
		Role:      domain.RoleAssistant,
		Content:   content,
		CreatedAt: time.Now(),
	}
	if err := a.store.SaveMessage(ctx, assistantMsg); err != nil {
		a.logger.Error("failed to save assistant message", zap.Error(err))
	}
}

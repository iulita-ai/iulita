package dashboard

import (
	"runtime"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"net/http"

	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/domain"
	ollamallm "github.com/iulita-ai/iulita/internal/llm/ollama"
	openaillm "github.com/iulita-ai/iulita/internal/llm/openai"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/storage"
	"github.com/iulita-ai/iulita/internal/version"
)

func (s *Server) handleSystem(c *fiber.Ctx) error {
	resp := fiber.Map{
		"app":        "iulita",
		"version":    version.String(),
		"uptime":     time.Since(s.startedAt).String(),
		"uptime_sec": int(time.Since(s.startedAt).Seconds()),
		"go_version": runtime.Version(),
		"started_at": s.startedAt.Format(time.RFC3339),
		"setup_mode": s.setupMode,
	}
	if s.configStore != nil {
		wizardCompleted := false
		if val, ok := s.configStore.Get("_system.wizard_completed"); ok && val == "true" {
			wizardCompleted = true
		}
		resp["wizard_completed"] = wizardCompleted
		resp["encryption_enabled"] = s.configStore.EncryptionEnabled()
	}
	return c.JSON(resp)
}

func (s *Server) handleStats(c *fiber.Ctx) error {
	ctx := c.Context()
	chatID := c.Query("chat_id")

	var msgCount int
	if chatID != "" {
		count, err := s.store.CountMessages(ctx, chatID)
		if err != nil {
			return s.errorResponse(c, err)
		}
		msgCount = count
	} else {
		chatIDs, err := s.store.GetChatIDs(ctx)
		if err != nil {
			return s.errorResponse(c, err)
		}
		for _, id := range chatIDs {
			count, err := s.store.CountMessages(ctx, id)
			if err != nil {
				return s.errorResponse(c, err)
			}
			msgCount += count
		}
	}

	facts, err := s.store.GetAllFacts(ctx, chatID)
	if err != nil {
		return s.errorResponse(c, err)
	}

	insightCount, err := s.store.CountInsights(ctx, chatID)
	if err != nil {
		return s.errorResponse(c, err)
	}

	reminders, err := s.store.ListAllReminders(ctx, chatID)
	if err != nil {
		return s.errorResponse(c, err)
	}

	techFactCount, err := s.store.CountTechFacts(ctx, chatID)
	if err != nil {
		return s.errorResponse(c, err)
	}

	return c.JSON(fiber.Map{
		"messages":   msgCount,
		"facts":      len(facts),
		"insights":   insightCount,
		"reminders":  len(reminders),
		"tech_facts": techFactCount,
	})
}

func (s *Server) handleChats(c *fiber.Ctx) error {
	ctx := c.Context()

	chatIDs, err := s.store.GetChatIDs(ctx)
	if err != nil {
		return s.errorResponse(c, err)
	}

	type chatInfo struct {
		ChatID   string `json:"chat_id"`
		Messages int    `json:"messages"`
	}

	result := make([]chatInfo, 0, len(chatIDs))
	for _, id := range chatIDs {
		count, err := s.store.CountMessages(ctx, id)
		if err != nil {
			return s.errorResponse(c, err)
		}
		result = append(result, chatInfo{ChatID: id, Messages: count})
	}

	return c.JSON(result)
}

func (s *Server) handleFacts(c *fiber.Ctx) error {
	ctx := c.Context()
	chatID := c.Query("chat_id")
	userID := c.Query("user_id")
	query := c.Query("q")
	limit := queryInt(c, "limit", 100)

	if query != "" {
		if userID != "" {
			facts, err := s.store.SearchFactsByUser(ctx, userID, query, limit)
			if err != nil {
				return s.errorResponse(c, err)
			}
			return c.JSON(facts)
		}
		if chatID != "" {
			facts, err := s.store.SearchFacts(ctx, chatID, query, limit)
			if err != nil {
				return s.errorResponse(c, err)
			}
			return c.JSON(facts)
		}
	}

	if userID != "" {
		facts, err := s.store.GetAllFactsByUser(ctx, userID)
		if err != nil {
			return s.errorResponse(c, err)
		}
		if limit > 0 && len(facts) > limit {
			facts = facts[len(facts)-limit:]
		}
		return c.JSON(facts)
	}

	facts, err := s.store.GetAllFacts(ctx, chatID)
	if err != nil {
		return s.errorResponse(c, err)
	}

	if limit > 0 && len(facts) > limit {
		// Return the most recent facts (GetAllFacts is ordered ASC).
		facts = facts[len(facts)-limit:]
	}

	return c.JSON(facts)
}

func (s *Server) handleInsights(c *fiber.Ctx) error {
	ctx := c.Context()
	chatID := c.Query("chat_id")
	userID := c.Query("user_id")
	limit := queryInt(c, "limit", 50)

	if userID != "" {
		insights, err := s.store.GetRecentInsightsByUser(ctx, userID, limit)
		if err != nil {
			return s.errorResponse(c, err)
		}
		return c.JSON(insights)
	}

	if chatID != "" {
		insights, err := s.store.GetRecentInsights(ctx, chatID, limit)
		if err != nil {
			return s.errorResponse(c, err)
		}
		return c.JSON(insights)
	}

	// No filter — collect insights from all users and chats.
	var allInsights []domain.Insight

	userIDs, _ := s.store.GetUserIDs(ctx)
	seen := make(map[int64]struct{})
	for _, uid := range userIDs {
		insights, err := s.store.GetRecentInsightsByUser(ctx, uid, limit)
		if err != nil {
			return s.errorResponse(c, err)
		}
		for _, d := range insights {
			if _, ok := seen[d.ID]; !ok {
				seen[d.ID] = struct{}{}
				allInsights = append(allInsights, d)
			}
		}
	}

	// Also collect chat-scoped insights (no user_id set).
	chatIDs, _ := s.store.GetChatIDs(ctx)
	for _, cid := range chatIDs {
		insights, err := s.store.GetRecentInsights(ctx, cid, limit)
		if err != nil {
			return s.errorResponse(c, err)
		}
		for _, d := range insights {
			if _, ok := seen[d.ID]; !ok {
				seen[d.ID] = struct{}{}
				allInsights = append(allInsights, d)
			}
		}
	}

	return c.JSON(allInsights)
}

func (s *Server) handleReminders(c *fiber.Ctx) error {
	ctx := c.Context()
	chatID := c.Query("chat_id")

	reminders, err := s.store.ListAllReminders(ctx, chatID)
	if err != nil {
		return s.errorResponse(c, err)
	}
	return c.JSON(reminders)
}

func (s *Server) handleDirectives(c *fiber.Ctx) error {
	ctx := c.Context()
	chatID := c.Query("chat_id")

	if chatID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "chat_id is required",
		})
	}

	directive, err := s.store.GetDirective(ctx, chatID)
	if err != nil {
		return s.errorResponse(c, err)
	}
	if directive == nil {
		return c.JSON(fiber.Map{"directive": nil})
	}
	return c.JSON(directive)
}

func (s *Server) handleMessages(c *fiber.Ctx) error {
	ctx := c.Context()
	chatID := c.Query("chat_id")
	limit := queryInt(c, "limit", 50)
	beforeID := int64(queryInt(c, "before_id", 0))

	if chatID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "chat_id is required",
		})
	}

	var messages []domain.ChatMessage
	var err error
	if beforeID > 0 {
		messages, err = s.store.GetHistoryBefore(ctx, chatID, beforeID, limit)
	} else {
		messages, err = s.store.GetHistory(ctx, chatID, limit)
	}
	if err != nil {
		return s.errorResponse(c, err)
	}
	return c.JSON(messages)
}

func (s *Server) handleSkills(c *fiber.Ctx) error {
	type skillInfo struct {
		Name            string `json:"name"`
		Description     string `json:"description"`
		Type            string `json:"type"`
		Enabled         bool   `json:"enabled"`
		HasCapabilities bool   `json:"has_capabilities"`
		HasConfig       bool   `json:"has_config"`
		ManifestGroup   string `json:"manifest_group,omitempty"`
	}

	var skills []skillInfo
	for _, ss := range s.registry.AllSkills() {
		group := ss.ManifestGroup
		hasConfig := group != "" && s.manifestHasConfig(group)
		skills = append(skills, skillInfo{
			Name:            ss.Skill.Name(),
			Description:     ss.Skill.Description(),
			Type:            "tool",
			Enabled:         ss.Enabled,
			HasCapabilities: ss.HasCapabilities,
			HasConfig:       hasConfig,
			ManifestGroup:   group,
		})
	}
	for _, m := range s.registry.Manifests() {
		if m.Type == skill.TextOnly {
			skills = append(skills, skillInfo{
				Name:            m.Name,
				Description:     m.Description,
				Type:            "text",
				Enabled:         true,
				HasCapabilities: true,
				HasConfig:       len(m.ConfigKeys) > 0,
			})
		}
	}

	return c.JSON(skills)
}

// manifestHasConfig returns true if a manifest has configurable keys.
func (s *Server) manifestHasConfig(manifestName string) bool {
	m, ok := s.registry.GetManifest(manifestName)
	return ok && len(m.ConfigKeys) > 0
}

// handleToggleSkill enables or disables a skill or skill group at runtime via config store.
// If the name matches a manifest group, all skills in the group are toggled.
func (s *Server) handleToggleSkill(c *fiber.Ctx) error {
	name := c.Params("name")
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}

	key := "skills." + name + ".enabled"
	username := "admin"
	claims := auth.GetClaims(c)
	if claims != nil {
		username = claims.Username
	}

	// Check if this is a manifest group toggle.
	isGroup := len(s.registry.GroupSkills(name)) > 0

	if body.Enabled {
		if s.configStore != nil {
			_ = s.configStore.Delete(c.Context(), key)
		}
		if isGroup {
			s.registry.EnableGroup(name)
		} else {
			s.registry.EnableSkill(name)
		}
	} else {
		if s.configStore != nil {
			if err := s.configStore.Set(c.Context(), key, "false", username, false); err != nil {
				return s.errorResponse(c, err)
			}
		}
		if isGroup {
			s.registry.DisableGroup(name)
		} else {
			s.registry.DisableSkill(name)
		}
	}

	s.logger.Info("skill toggled",
		zap.String("skill", name),
		zap.Bool("enabled", body.Enabled),
		zap.Bool("group", isGroup),
		zap.String("by", username),
	)

	return c.JSON(fiber.Map{"status": "ok", "skill": name, "enabled": body.Enabled, "group": isGroup})
}

// handleGetSkillConfig returns the config schema for a skill with current values.
func (s *Server) handleGetSkillConfig(c *fiber.Ctx) error {
	name := c.Params("name")

	// Resolve manifest: name can be a manifest name directly or a skill name.
	manifestName := s.resolveManifest(name)
	if manifestName == "" {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "skill not found or has no config"})
	}

	m, ok := s.registry.GetManifest(manifestName)
	if !ok {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "manifest not found"})
	}
	if len(m.ConfigKeys) == 0 {
		return c.JSON(fiber.Map{
			"skill":              name,
			"manifest":           manifestName,
			"description":        m.Description,
			"schema":             []any{},
			"encryption_enabled": s.configStore != nil && s.configStore.EncryptionEnabled(),
		})
	}

	fields, _ := s.registry.GetSkillConfigSchema(manifestName, s.configStore)
	return c.JSON(fiber.Map{
		"skill":              name,
		"manifest":           manifestName,
		"description":        m.Description,
		"schema":             fields,
		"encryption_enabled": s.configStore != nil && s.configStore.EncryptionEnabled(),
	})
}

// handleSetSkillConfig sets a config value for a skill key.
func (s *Server) handleSetSkillConfig(c *fiber.Ctx) error {
	name := c.Params("name")
	key := c.Params("key")

	if s.configStore == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "config store not available"})
	}

	// Verify the key belongs to this skill's manifest.
	manifestName := s.resolveManifest(name)
	if manifestName == "" {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "skill not found or has no config"})
	}
	m, ok := s.registry.GetManifest(manifestName)
	if !ok {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "manifest not found"})
	}
	keyValid := false
	for _, ck := range m.ConfigKeys {
		if ck == key {
			keyValid = true
			break
		}
	}
	if !keyValid {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "key does not belong to this skill"})
	}

	var body struct {
		Value string `json:"value"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}

	username := "admin"
	claims := auth.GetClaims(c)
	if claims != nil {
		username = claims.Username
	}

	// Store.Set auto-forces encrypt for secret keys; non-secrets default to plain.
	encrypt := s.configStore.IsSecretKey(key)
	if err := s.configStore.Set(c.Context(), key, body.Value, username, encrypt); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}

	s.logger.Info("skill config set",
		zap.String("skill", name),
		zap.String("key", key),
		zap.Bool("encrypted", encrypt),
		zap.String("by", username),
	)

	return c.JSON(fiber.Map{"status": "ok", "key": key})
}

// resolveManifest finds the manifest name for a skill or manifest name.
// Uses the registry's skill-to-manifest mapping, then falls back to direct manifest lookup.
func (s *Server) resolveManifest(name string) string {
	// Check skill-to-manifest mapping (e.g. "craft_read" → "craft").
	if mName := s.registry.SkillManifest(name); mName != "" {
		return mName
	}
	// Direct manifest lookup (e.g. "craft" is already a manifest name).
	if _, ok := s.registry.GetManifest(name); ok {
		return name
	}
	return ""
}

func (s *Server) handleTechFacts(c *fiber.Ctx) error {
	ctx := c.Context()
	chatID := c.Query("chat_id")

	facts, err := s.store.GetTechFacts(ctx, chatID)
	if err != nil {
		return s.errorResponse(c, err)
	}

	grouped := make(map[string][]fiber.Map)
	for _, f := range facts {
		grouped[f.Category] = append(grouped[f.Category], fiber.Map{
			"id":           f.ID,
			"key":          f.Key,
			"value":        f.Value,
			"confidence":   f.Confidence,
			"update_count": f.UpdateCount,
			"updated_at":   f.UpdatedAt,
		})
	}

	return c.JSON(grouped)
}

// --- Scheduler & Task endpoints ---

func (s *Server) handleSchedulersStatus(c *fiber.Ctx) error {
	if s.taskScheduler == nil {
		return c.JSON(fiber.Map{})
	}
	return c.JSON(s.taskScheduler.JobStatus(c.Context()))
}

func (s *Server) handleTriggerJob(c *fiber.Ctx) error {
	if s.taskScheduler == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "scheduler not configured"})
	}
	name := c.Params("name")
	if err := s.taskScheduler.TriggerJob(c.Context(), name); err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"status": "triggered"})
}

func (s *Server) handleListTasks(c *fiber.Ctx) error {
	statusStr := c.Query("status")
	taskType := c.Query("type")
	limit := queryInt(c, "limit", 50)

	filter := storage.TaskFilter{
		Type:  taskType,
		Limit: limit,
	}
	if statusStr != "" {
		st := domain.TaskStatus(statusStr)
		filter.Status = &st
	}

	tasks, err := s.store.ListTasks(c.Context(), filter)
	if err != nil {
		return s.errorResponse(c, err)
	}
	if tasks == nil {
		tasks = []domain.Task{}
	}
	return c.JSON(tasks)
}

func (s *Server) handleTaskCounts(c *fiber.Ctx) error {
	counts, err := s.store.CountTasksByStatus(c.Context())
	if err != nil {
		return s.errorResponse(c, err)
	}
	return c.JSON(counts)
}

func (s *Server) handleClaimTask(c *fiber.Ctx) error {
	var body struct {
		WorkerID     string   `json:"worker_id"`
		Capabilities []string `json:"capabilities"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if body.WorkerID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "worker_id required"})
	}

	task, err := s.store.ClaimTask(c.Context(), body.WorkerID, body.Capabilities)
	if err != nil {
		return s.errorResponse(c, err)
	}
	if task == nil {
		return c.Status(fiber.StatusNoContent).Send(nil)
	}
	return c.JSON(task)
}

func (s *Server) handleStartTask(c *fiber.Ctx) error {
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}
	var body struct {
		WorkerID string `json:"worker_id"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if err := s.store.StartTask(c.Context(), id, body.WorkerID); err != nil {
		return s.errorResponse(c, err)
	}
	return c.JSON(fiber.Map{"status": "started"})
}

func (s *Server) handleCompleteTask(c *fiber.Ctx) error {
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}
	var body struct {
		Result string `json:"result"`
	}
	c.BodyParser(&body)
	if err := s.store.CompleteTask(c.Context(), id, body.Result); err != nil {
		return s.errorResponse(c, err)
	}
	return c.JSON(fiber.Map{"status": "completed"})
}

func (s *Server) handleFailTask(c *fiber.Ctx) error {
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}
	var body struct {
		Error string `json:"error"`
	}
	c.BodyParser(&body)
	if err := s.store.FailTask(c.Context(), id, body.Error); err != nil {
		return s.errorResponse(c, err)
	}
	return c.JSON(fiber.Map{"status": "failed"})
}

// --- Config endpoints ---

func (s *Server) handleGetConfigSchema(c *fiber.Ctx) error {
	schema := config.CoreConfigSchema()
	// Enrich with effective values.
	type fieldWithValue struct {
		config.ConfigField
		Value       string `json:"value"`
		HasValue    bool   `json:"has_value"`
		HasOverride bool   `json:"has_override"`
	}
	type sectionResp struct {
		Name        string           `json:"name"`
		Label       string           `json:"label"`
		Description string           `json:"description"`
		Fields      []fieldWithValue `json:"fields"`
	}

	var sections []sectionResp
	for _, sec := range schema {
		var fields []fieldWithValue
		for _, f := range sec.Fields {
			fv := fieldWithValue{ConfigField: f}
			if s.configStore != nil {
				val, ok := s.configStore.GetEffective(f.Key)
				fv.HasValue = ok && val != ""
				fv.HasOverride = s.configStore.HasOverride(f.Key)
				if f.Secret {
					fv.Value = ""
				} else {
					fv.Value = val
				}
			}
			fields = append(fields, fv)
		}
		sections = append(sections, sectionResp{
			Name:        sec.Name,
			Label:       sec.Label,
			Description: sec.Description,
			Fields:      fields,
		})
	}
	return c.JSON(fiber.Map{
		"sections":           sections,
		"encryption_enabled": s.configStore != nil && s.configStore.EncryptionEnabled(),
	})
}

// handleListModels fetches available models from a provider dynamically.
// GET /api/config/models/:provider — provider is "openai", "ollama", or "claude"
func (s *Server) handleListModels(c *fiber.Ctx) error {
	provider := c.Params("provider")
	httpClient := &http.Client{}

	switch provider {
	case "claude":
		// Claude has no model listing API — return hardcoded list from schema.
		sec, ok := config.GetSection("claude")
		if !ok {
			return c.JSON(fiber.Map{"models": []string{}})
		}
		for _, f := range sec.Fields {
			if f.Key == "claude.model" {
				return c.JSON(fiber.Map{"models": f.Options, "source": "static"})
			}
		}
		return c.JSON(fiber.Map{"models": []string{}})

	case "openai":
		apiKey, _ := s.configStore.GetEffective("openai.api_key")
		if apiKey == "" {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "openai.api_key not configured"})
		}
		baseURL, _ := s.configStore.GetEffective("openai.base_url")
		models, err := openaillm.ListModels(baseURL, apiKey, httpClient)
		if err != nil {
			return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"models": models, "source": "dynamic"})

	case "ollama":
		ollamaURL, _ := s.configStore.GetEffective("ollama.url")
		if ollamaURL == "" {
			ollamaURL = "http://localhost:11434"
		}
		models, err := ollamallm.ListModels(ollamaURL, httpClient)
		if err != nil {
			return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"models": models, "source": "dynamic"})

	default:
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "unknown provider: " + provider})
	}
}

func (s *Server) handleListConfig(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{
		"overrides":          s.configStore.List(),
		"encryption_enabled": s.configStore.EncryptionEnabled(),
	})
}

func (s *Server) handleListConfigDecrypted(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{
		"overrides":          s.configStore.ListDecrypted(),
		"encryption_enabled": s.configStore.EncryptionEnabled(),
	})
}

func (s *Server) handleSetConfig(c *fiber.Ctx) error {
	key := c.Params("key")
	var body struct {
		Value   string `json:"value"`
		Encrypt bool   `json:"encrypt"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if body.Value == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "value is required"})
	}
	username := "admin"
	claims := auth.GetClaims(c)
	if claims != nil {
		username = claims.Username
	}
	if err := s.configStore.Set(c.Context(), key, body.Value, username, body.Encrypt); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"status": "ok", "key": key})
}

func (s *Server) handleDeleteConfig(c *fiber.Ctx) error {
	key := c.Params("key")
	if err := s.configStore.Delete(c.Context(), key); err != nil {
		return s.errorResponse(c, err)
	}
	return c.JSON(fiber.Map{"status": "deleted", "key": key})
}

func (s *Server) handleUsageSummary(c *fiber.Ctx) error {
	ctx := c.Context()
	chatID := c.Query("chat_id")

	chatIDs := []string{chatID}
	if chatID == "" {
		ids, err := s.store.GetChatIDs(ctx)
		if err != nil {
			return s.errorResponse(c, err)
		}
		chatIDs = ids
	}

	var totalInput, totalOutput, totalRequests int64
	type chatUsage struct {
		ChatID       string `json:"chat_id"`
		InputTokens  int64  `json:"input_tokens"`
		OutputTokens int64  `json:"output_tokens"`
		Requests     int64  `json:"requests"`
	}

	var perChat []chatUsage
	for _, id := range chatIDs {
		records, err := s.store.GetUsageStats(ctx, id)
		if err != nil {
			return s.errorResponse(c, err)
		}
		var input, output, reqs int64
		for _, r := range records {
			input += r.InputTokens
			output += r.OutputTokens
			reqs += r.Requests
		}
		totalInput += input
		totalOutput += output
		totalRequests += reqs
		perChat = append(perChat, chatUsage{
			ChatID:       id,
			InputTokens:  input,
			OutputTokens: output,
			Requests:     reqs,
		})
	}

	// Rough cost estimate based on Claude Sonnet pricing:
	// Input: $3/MTok, Output: $15/MTok
	estimatedCost := float64(totalInput)/1_000_000*3.0 + float64(totalOutput)/1_000_000*15.0

	return c.JSON(fiber.Map{
		"total_input_tokens":  totalInput,
		"total_output_tokens": totalOutput,
		"total_requests":      totalRequests,
		"estimated_cost_usd":  estimatedCost,
		"per_chat":            perChat,
	})
}

func (s *Server) handleDeleteFact(c *fiber.Ctx) error {
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}

	// Support legacy chat_id filter, but no longer require it.
	chatID := c.Query("chat_id")
	if chatID != "" {
		if err := s.store.DeleteFact(c.Context(), id, chatID); err != nil {
			return s.errorResponse(c, err)
		}
	} else {
		if err := s.store.DeleteFactByID(c.Context(), id); err != nil {
			return s.errorResponse(c, err)
		}
	}
	return c.JSON(fiber.Map{"status": "deleted", "id": id})
}

func (s *Server) handleUpdateFact(c *fiber.Ctx) error {
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if body.Content == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "content is required"})
	}

	if err := s.store.UpdateFactContent(c.Context(), id, body.Content); err != nil {
		return s.errorResponse(c, err)
	}
	return c.JSON(fiber.Map{"status": "updated", "id": id})
}

func (s *Server) handleSearchFacts(c *fiber.Ctx) error {
	ctx := c.Context()
	chatID := c.Query("chat_id")
	query := c.Query("q")
	limit := queryInt(c, "limit", 50)

	if query == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "q parameter is required"})
	}
	if chatID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "chat_id is required"})
	}

	facts, err := s.store.SearchFacts(ctx, chatID, query, limit)
	if err != nil {
		return s.errorResponse(c, err)
	}
	if facts == nil {
		facts = []domain.Fact{}
	}
	return c.JSON(facts)
}

func (s *Server) errorResponse(c *fiber.Ctx, err error) error {
	s.logger.Error("API error",
		zap.String("method", c.Method()),
		zap.String("path", c.Path()),
		zap.Error(err),
	)
	return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
		"error": err.Error(),
	})
}

func queryInt(c *fiber.Ctx, key string, defaultVal int) int {
	s := c.Query(key)
	if s == "" {
		return defaultVal
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return defaultVal
	}
	return v
}

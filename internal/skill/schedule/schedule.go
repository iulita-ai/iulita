// Package schedule provides a skill that lets a user create and manage recurring
// scheduled jobs ("self-scheduling") from chat. Each job runs a prompt on a cron
// or interval and delivers the result back to the chat. Jobs are scoped to the
// creating user; a per-user cap guards against abuse.
package schedule

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/robfig/cron/v3"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/storage"
)

//go:embed SKILL.md
var skillFS embed.FS

// LoadManifest reads the embedded SKILL.md and returns the skill manifest.
func LoadManifest() (*skill.Manifest, error) {
	return skill.LoadManifestFromFS(skillFS, "SKILL.md")
}

const (
	// defaultMaxJobsPerUser bounds how many scheduled jobs a single user may own.
	defaultMaxJobsPerUser = 20
	// maxNameLen / maxPromptLen bound free-text fields.
	maxNameLen   = 120
	maxPromptLen = 4000
)

// cronParser matches the 5-field standard cron the scheduler uses (and accepts a
// leading CRON_TZ= descriptor so a user's "9am" is honored in their timezone).
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// Skill implements the `schedule` tool for user self-scheduling of agent jobs.
type Skill struct {
	store storage.Repository

	mu             sync.RWMutex
	maxJobsPerUser int

	// createMu serializes the per-user cap check + insert so concurrent creates
	// from the same user (e.g. two channels) can't both pass the cap. The app is
	// single-instance (local SQLite), so an in-process lock fully closes the race.
	createMu sync.Mutex
}

// New creates a new schedule skill.
func New(store storage.Repository) *Skill {
	return &Skill{store: store, maxJobsPerUser: defaultMaxJobsPerUser}
}

// Name returns the skill name.
func (s *Skill) Name() string { return "schedule" }

// Description returns a human-readable description of the skill.
func (s *Skill) Description() string {
	return "Create and manage your own recurring scheduled jobs that run a prompt on a cron or interval and deliver the result to this chat. Actions: create, list, pause, resume, delete."
}

// InputSchema returns the JSON schema for the skill's input.
func (s *Skill) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {"type": "string", "enum": ["create","list","pause","resume","delete"], "description": "Operation to perform"},
			"name": {"type": "string", "description": "Short label for the job (create)"},
			"prompt": {"type": "string", "description": "The instruction the job runs each time, e.g. 'Check my calendar for today and summarize'. The job runs with the user's tools and memory (create)"},
			"cron_expr": {"type": "string", "description": "5-field standard cron, e.g. '0 9 * * 1-5' for weekdays at 09:00. Provide this for recurring time-of-day schedules (create)"},
			"interval": {"type": "string", "description": "Go duration fallback when no cron, e.g. '6h', '30m' (create)"},
			"timezone": {"type": "string", "description": "IANA timezone for the cron, e.g. 'Europe/Helsinki'. Defaults to UTC. Use the user's timezone so 'at 9am' means their local time (create)"},
			"wake_gate_prompt": {"type": "string", "description": "Optional cheap yes/no pre-check evaluated before each run; if it answers no, the run is skipped. Reasons only over the user's recent memory, not live tools (create)"},
			"id": {"type": "integer", "description": "Job ID (pause/resume/delete)"}
		},
		"required": ["action"]
	}`)
}

type input struct {
	Action         string `json:"action"`
	Name           string `json:"name"`
	Prompt         string `json:"prompt"`
	CronExpr       string `json:"cron_expr"`
	Interval       string `json:"interval"`
	Timezone       string `json:"timezone"`
	WakeGatePrompt string `json:"wake_gate_prompt"`
	ID             int64  `json:"id"`
}

// Execute dispatches the requested action.
func (s *Skill) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in input
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	// user_id is ALWAYS derived server-side from context, never from input.
	userID := skill.UserIDFrom(ctx)
	if userID == "" {
		return "", fmt.Errorf("scheduling requires an identified user")
	}
	isAdmin := skill.UserRoleFrom(ctx) == string(domain.RoleAdmin)

	switch in.Action {
	case "create":
		return s.create(ctx, in, userID, skill.ChatIDFrom(ctx))
	case "list":
		return s.list(ctx, userID, isAdmin)
	case "pause":
		return s.setEnabled(ctx, in.ID, userID, isAdmin, false)
	case "resume":
		return s.setEnabled(ctx, in.ID, userID, isAdmin, true)
	case "delete":
		return s.deleteJob(ctx, in.ID, userID, isAdmin)
	default:
		return "", fmt.Errorf("unknown action %q, use create/list/pause/resume/delete", in.Action)
	}
}

func (s *Skill) create(ctx context.Context, in input, userID, chatID string) (string, error) {
	name := strings.TrimSpace(in.Name)
	prompt := strings.TrimSpace(in.Prompt)
	if name == "" {
		return "", fmt.Errorf("name is required for create")
	}
	if prompt == "" {
		return "", fmt.Errorf("prompt is required for create")
	}
	name = clampRunes(name, maxNameLen)
	if utf8.RuneCountInString(prompt) > maxPromptLen {
		return "", fmt.Errorf("prompt too long (max %d chars)", maxPromptLen)
	}
	if chatID == "" {
		return "", fmt.Errorf("cannot determine where to deliver results (no chat in context)")
	}

	cronExpr, interval, err := normalizeSchedule(in)
	if err != nil {
		return "", err
	}

	// Serialize the cap check + insert so concurrent creates from the same user
	// can't both pass the cap (TOCTOU).
	s.createMu.Lock()
	defer s.createMu.Unlock()

	s.mu.RLock()
	limit := s.maxJobsPerUser
	s.mu.RUnlock()
	count, err := s.store.CountAgentJobsByUser(ctx, userID)
	if err != nil {
		return "", err
	}
	if count >= limit {
		return "", fmt.Errorf("you already have %d scheduled jobs (limit %d) — delete one first", count, limit)
	}

	job := &domain.AgentJob{
		UserID:         userID,
		Name:           name,
		Prompt:         prompt,
		CronExpr:       cronExpr,
		Interval:       interval,
		DeliveryChatID: chatID, // always the chat the user is speaking in
		WakeGatePrompt: strings.TrimSpace(in.WakeGatePrompt),
		Enabled:        true,
	}
	if err := s.store.CreateAgentJob(ctx, job); err != nil {
		return "", err
	}

	when := interval
	if cronExpr != "" {
		when = "cron " + cronExpr
	}
	return fmt.Sprintf("Scheduled job #%d %q created (%s). Results will be delivered here. Use list/pause/resume/delete to manage it.", job.ID, name, when), nil
}

// normalizeSchedule validates the requested schedule and returns the cron
// expression (possibly with a CRON_TZ prefix) and interval to store. Exactly one
// of cron/interval is populated; cron takes precedence.
func normalizeSchedule(in input) (cronExpr, interval string, err error) {
	cronRaw := strings.TrimSpace(in.CronExpr)
	intervalRaw := strings.TrimSpace(in.Interval)
	if cronRaw == "" && intervalRaw == "" {
		return "", "", fmt.Errorf("provide either cron_expr (e.g. '0 9 * * 1-5') or interval (e.g. '6h')")
	}

	if cronRaw != "" {
		if _, perr := cronParser.Parse(cronRaw); perr != nil {
			return "", "", fmt.Errorf("invalid cron_expr %q: %w", cronRaw, perr)
		}
		stored := cronRaw
		if tz := strings.TrimSpace(in.Timezone); tz != "" && tz != "UTC" {
			loc, lerr := time.LoadLocation(tz)
			if lerr != nil {
				return "", "", fmt.Errorf("unknown timezone %q: %w", tz, lerr)
			}
			stored = fmt.Sprintf("CRON_TZ=%s %s", loc.String(), cronRaw)
			if _, perr := cronParser.Parse(stored); perr != nil {
				return "", "", fmt.Errorf("invalid cron with timezone: %w", perr)
			}
		}
		return stored, "24h", nil // interval kept as a harmless fallback default
	}

	d, derr := time.ParseDuration(intervalRaw)
	if derr != nil || d <= 0 {
		return "", "", fmt.Errorf("invalid interval %q, use a Go duration like '6h' or '30m'", intervalRaw)
	}
	if d < time.Minute {
		return "", "", fmt.Errorf("interval too short (minimum 1m)")
	}
	return "", intervalRaw, nil
}

func (s *Skill) list(ctx context.Context, userID string, isAdmin bool) (string, error) {
	var (
		jobs []domain.AgentJob
		err  error
	)
	if isAdmin {
		jobs, err = s.store.ListAgentJobs(ctx)
	} else {
		jobs, err = s.store.ListAgentJobsByUser(ctx, userID)
	}
	if err != nil {
		return "", err
	}
	if len(jobs) == 0 {
		return "You have no scheduled jobs.", nil
	}

	var b strings.Builder
	b.WriteString("Scheduled jobs:\n")
	for i := range jobs {
		j := &jobs[i]
		status := "enabled"
		if !j.Enabled {
			status = "paused"
		}
		when := j.Interval
		if j.CronExpr != "" {
			when = j.CronExpr
		}
		next := "—"
		if !j.NextRun.IsZero() {
			next = j.NextRun.UTC().Format("2006-01-02 15:04 UTC")
		}
		owner := ""
		if isAdmin {
			owner = fmt.Sprintf(" owner=%s", shortID(j.UserID))
		}
		fmt.Fprintf(&b, "#%d %q [%s] %s · next %s%s\n", j.ID, j.Name, status, when, next, owner)
	}
	return b.String(), nil
}

func (s *Skill) setEnabled(ctx context.Context, id int64, userID string, isAdmin, enabled bool) (string, error) {
	job, err := s.ownedJob(ctx, id, userID, isAdmin)
	if err != nil {
		return "", err
	}
	job.Enabled = enabled
	if err := s.store.UpdateAgentJob(ctx, job); err != nil {
		return "", err
	}
	verb := "paused"
	if enabled {
		verb = "resumed"
	}
	return fmt.Sprintf("Job #%d %q %s.", job.ID, job.Name, verb), nil
}

func (s *Skill) deleteJob(ctx context.Context, id int64, userID string, isAdmin bool) (string, error) {
	job, err := s.ownedJob(ctx, id, userID, isAdmin)
	if err != nil {
		return "", err
	}
	if err := s.store.DeleteAgentJob(ctx, job.ID); err != nil {
		return "", err
	}
	return fmt.Sprintf("Job #%d %q deleted.", job.ID, job.Name), nil
}

// ownedJob fetches a job and enforces ownership: a non-admin may only touch their
// own jobs. Returns a uniform "not found" error otherwise (no existence leak).
func (s *Skill) ownedJob(ctx context.Context, id int64, userID string, isAdmin bool) (*domain.AgentJob, error) {
	if id == 0 {
		return nil, fmt.Errorf("id is required")
	}
	job, err := s.store.GetAgentJob(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("job #%d not found", id)
	}
	if !isAdmin && job.UserID != userID {
		return nil, fmt.Errorf("job #%d not found", id)
	}
	return job, nil
}

// OnConfigChanged implements skill.ConfigReloadable for hot-reloading the cap.
func (s *Skill) OnConfigChanged(key, value string) {
	if key != "skills.schedule.max_jobs_per_user" {
		return
	}
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n <= 0 {
		return
	}
	s.mu.Lock()
	s.maxJobsPerUser = n
	s.mu.Unlock()
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// clampRunes truncates s to at most limit runes (rune-safe).
func clampRunes(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit])
}

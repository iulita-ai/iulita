package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/scheduler"
	"github.com/iulita-ai/iulita/internal/storage"
)

// InsightJob creates insight.generate tasks for each chat with enough facts.
func InsightJob(store storage.Repository, cfg config.InsightsConfig, logger *zap.Logger) scheduler.JobDefinition {
	minFacts := cfg.MinFacts
	if minFacts <= 0 {
		minFacts = 20
	}
	interval := 24 * time.Hour
	if cfg.Interval != "" {
		if d, err := time.ParseDuration(cfg.Interval); err == nil {
			interval = d
		}
	}

	return scheduler.JobDefinition{
		Name:     "insights",
		Interval: interval,
		Enabled:  cfg.Enabled,
		CreateTasks: func(ctx context.Context) []domain.Task {
			allFacts, err := store.GetAllFacts(ctx, "")
			if err != nil {
				logger.Error("insight job: failed to get facts", zap.Error(err))
				return nil
			}

			// Group by UserID first, fall back to ChatID for facts without a user.
			type scopeKey struct {
				userID string
				chatID string
			}
			scopeCounts := make(map[scopeKey]int)
			for _, f := range allFacts {
				if f.UserID != "" {
					scopeCounts[scopeKey{userID: f.UserID}]++
				} else {
					scopeCounts[scopeKey{chatID: f.ChatID}]++
				}
			}

			var tasks []domain.Task
			for sk, count := range scopeCounts {
				if count < minFacts {
					continue
				}
				p := insightPayload{ChatID: sk.chatID, UserID: sk.userID}
				payload, _ := json.Marshal(p)
				key := sk.chatID
				if sk.userID != "" {
					key = "user:" + sk.userID
				}
				tasks = append(tasks, domain.Task{
					Type:         TaskTypeInsightGenerate,
					Payload:      string(payload),
					Capabilities: "llm,storage",
					MaxAttempts:  2,
					UniqueKey:    fmt.Sprintf("insight.generate:%s", key),
				})
			}
			return tasks
		},
	}
}

// InsightCleanupJob creates periodic insight cleanup tasks.
func InsightCleanupJob(cfg config.InsightsConfig) scheduler.JobDefinition {
	return scheduler.JobDefinition{
		Name:     "insight_cleanup",
		Interval: 1 * time.Hour,
		Enabled:  cfg.Enabled,
		CreateTasks: func(_ context.Context) []domain.Task {
			return []domain.Task{{
				Type:         TaskTypeInsightCleanup,
				Payload:      "{}",
				Capabilities: "storage",
				MaxAttempts:  1,
				UniqueKey:    "insight.cleanup",
			}}
		},
	}
}

// TechFactJob creates techfact.analyze tasks for each chat.
func TechFactJob(store storage.Repository, cfg config.TechFactsConfig, logger *zap.Logger) scheduler.JobDefinition {
	interval := 6 * time.Hour
	if cfg.Interval != "" {
		if d, err := time.ParseDuration(cfg.Interval); err == nil {
			interval = d
		}
	}

	return scheduler.JobDefinition{
		Name:     "techfacts",
		Interval: interval,
		Enabled:  cfg.Enabled,
		CreateTasks: func(ctx context.Context) []domain.Task {
			chatIDs, err := store.GetChatIDs(ctx)
			if err != nil {
				logger.Error("techfact job: failed to get chat IDs", zap.Error(err))
				return nil
			}

			var tasks []domain.Task
			for _, chatID := range chatIDs {
				payload, _ := json.Marshal(techFactPayload{ChatID: chatID})
				tasks = append(tasks, domain.Task{
					Type:         TaskTypeTechFactAnalyze,
					Payload:      string(payload),
					Capabilities: "llm,storage",
					MaxAttempts:  2,
					UniqueKey:    fmt.Sprintf("techfact.analyze:%s", chatID),
				})
			}
			return tasks
		},
	}
}

// HeartbeatJob creates heartbeat.check tasks for configured chats.
func HeartbeatJob(cfg config.HeartbeatConfig) scheduler.JobDefinition {
	interval := 6 * time.Hour
	if cfg.Interval != "" {
		if d, err := time.ParseDuration(cfg.Interval); err == nil {
			interval = d
		}
	}

	return scheduler.JobDefinition{
		Name:     "heartbeat",
		Interval: interval,
		Enabled:  cfg.Enabled,
		CreateTasks: func(_ context.Context) []domain.Task {
			var tasks []domain.Task
			for _, chatID := range cfg.ChatIDs {
				payload, _ := json.Marshal(heartbeatPayload{ChatID: chatID})
				tasks = append(tasks, domain.Task{
					Type:         TaskTypeHeartbeat,
					Payload:      string(payload),
					Capabilities: "llm,storage,telegram",
					MaxAttempts:  2,
					UniqueKey:    fmt.Sprintf("heartbeat.check:%s", chatID),
				})
			}
			return tasks
		},
	}
}

// AgentJobsJob creates agent.job tasks for each due user-defined agent job.
func AgentJobsJob(store storage.Repository, logger *zap.Logger) scheduler.JobDefinition {
	return scheduler.JobDefinition{
		Name:     "agent_jobs",
		Interval: 30 * time.Second,
		Enabled:  true,
		CreateTasks: func(ctx context.Context) []domain.Task {
			jobs, err := store.GetDueAgentJobs(ctx, time.Now())
			if err != nil {
				logger.Error("agent jobs: failed to get due jobs", zap.Error(err))
				return nil
			}

			var tasks []domain.Task
			for _, j := range jobs {
				payload, _ := json.Marshal(agentJobTaskPayload{
					JobID:          j.ID,
					JobName:        j.Name,
					Prompt:         j.Prompt,
					DeliveryChatID: j.DeliveryChatID,
				})
				tasks = append(tasks, domain.Task{
					Type:         TaskTypeAgentJob,
					Payload:      string(payload),
					Capabilities: "llm",
					MaxAttempts:  2,
					UniqueKey:    fmt.Sprintf("agent.job:%d", j.ID),
				})

				// Calculate next run.
				nextRun := computeNextRun(j.CronExpr, j.Interval)
				if err := store.UpdateAgentJobSchedule(ctx, j.ID, time.Now(), nextRun); err != nil {
					logger.Error("agent jobs: failed to update schedule", zap.Int64("id", j.ID), zap.Error(err))
				}
			}
			return tasks
		},
	}
}

type agentJobTaskPayload struct {
	JobID          int64  `json:"job_id"`
	JobName        string `json:"job_name"`
	Prompt         string `json:"prompt"`
	DeliveryChatID string `json:"delivery_chat_id"`
}

func computeNextRun(cronExpr, interval string) time.Time {
	now := time.Now()

	if cronExpr != "" {
		parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		sched, err := parser.Parse(cronExpr)
		if err == nil {
			return sched.Next(now)
		}
	}

	d, err := time.ParseDuration(interval)
	if err != nil || d <= 0 {
		d = 24 * time.Hour
	}
	return now.Add(d)
}

// ReminderJob creates reminder.fire tasks for due reminders.
func ReminderJob(store storage.Repository, logger *zap.Logger) scheduler.JobDefinition {
	return scheduler.JobDefinition{
		Name:     "reminders",
		Interval: 30 * time.Second,
		Enabled:  true,
		CreateTasks: func(ctx context.Context) []domain.Task {
			reminders, err := store.GetDueReminders(ctx, time.Now())
			if err != nil {
				logger.Error("reminder job: failed to get due reminders", zap.Error(err))
				return nil
			}

			var tasks []domain.Task
			for _, r := range reminders {
				payload, _ := json.Marshal(reminderPayload{
					ReminderID: r.ID,
					ChatID:     r.ChatID,
					Title:      r.Title,
					DueAt:      r.DueAt.Format(time.RFC3339),
					Timezone:   r.Timezone,
				})
				tasks = append(tasks, domain.Task{
					Type:         TaskTypeReminderFire,
					Payload:      string(payload),
					Capabilities: "telegram,storage",
					MaxAttempts:  3,
					UniqueKey:    fmt.Sprintf("reminder.fire:%d", r.ID),
				})
			}
			return tasks
		},
	}
}

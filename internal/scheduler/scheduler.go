package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/robfig/cron/v3"
	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage"
)

// JobDefinition describes a recurring job that produces tasks.
type JobDefinition struct {
	Name        string
	Interval    time.Duration
	CronExpr    string // optional cron expression (overrides Interval when set)
	Timezone    string // optional timezone for cron (e.g. "Europe/Berlin"), defaults to UTC
	Enabled     bool
	CreateTasks func(ctx context.Context) []domain.Task
}

// Scheduler creates tasks on schedule and performs maintenance.
type Scheduler struct {
	store        storage.Repository
	jobs         []JobDefinition
	pollInterval time.Duration
	staleTimeout time.Duration
	taskTTL      time.Duration
	logger       *zap.Logger
}

// SchedulerConfig configures the scheduler.
type SchedulerConfig struct {
	PollInterval time.Duration // how often to check for due jobs (default 30s)
	StaleTimeout time.Duration // reclaim tasks stuck longer than this (default 5m)
	TaskTTL      time.Duration // delete completed/failed tasks older than this (default 7d)
}

// NewScheduler creates a new task scheduler (coordinator).
func NewScheduler(store storage.Repository, cfg SchedulerConfig, logger *zap.Logger) *Scheduler {
	pollInterval := cfg.PollInterval
	if pollInterval <= 0 {
		pollInterval = 30 * time.Second
	}
	staleTimeout := cfg.StaleTimeout
	if staleTimeout <= 0 {
		staleTimeout = 5 * time.Minute
	}
	taskTTL := cfg.TaskTTL
	if taskTTL <= 0 {
		taskTTL = 7 * 24 * time.Hour
	}

	return &Scheduler{
		store:        store,
		pollInterval: pollInterval,
		staleTimeout: staleTimeout,
		taskTTL:      taskTTL,
		logger:       logger,
	}
}

// RegisterJob adds a job definition to the scheduler.
func (s *Scheduler) RegisterJob(job JobDefinition) {
	s.jobs = append(s.jobs, job)
}

// Start runs the scheduler loop. Blocks until ctx is cancelled.
func (s *Scheduler) Start(ctx context.Context) error {
	s.logger.Info("scheduler started",
		zap.Int("jobs", len(s.jobs)),
		zap.Duration("poll_interval", s.pollInterval))

	// Initialize scheduler states for all jobs.
	for _, job := range s.jobs {
		if err := s.initJobState(ctx, job); err != nil {
			s.logger.Error("failed to init job state", zap.String("job", job.Name), zap.Error(err))
		}
	}

	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()

	// Run an initial tick.
	s.tick(ctx)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

// TriggerJob forces an immediate run of a named job.
// Creates tasks with high priority.
func (s *Scheduler) TriggerJob(ctx context.Context, jobName string) error {
	for _, job := range s.jobs {
		if job.Name == jobName {
			tasks := job.CreateTasks(ctx)
			created := 0
			for i := range tasks {
				tasks[i].Priority = 1 // high priority for manual triggers
				ok, err := s.store.CreateTaskIfNotExists(ctx, &tasks[i])
				if err != nil {
					s.logger.Error("failed to create triggered task",
						zap.String("job", jobName), zap.Error(err))
					continue
				}
				if ok {
					created++
				}
			}
			s.logger.Info("job triggered manually",
				zap.String("job", jobName),
				zap.Int("tasks_created", created))
			return nil
		}
	}
	return errors.New("job not found: " + jobName)
}

// JobStatus returns the current status of all registered jobs.
func (s *Scheduler) JobStatus(ctx context.Context) []JobInfo {
	var infos []JobInfo
	for _, job := range s.jobs {
		info := JobInfo{
			Name:     job.Name,
			Interval: job.Interval.String(),
			CronExpr: job.CronExpr,
			Timezone: job.Timezone,
			Enabled:  job.Enabled,
		}
		if state, err := s.store.GetSchedulerState(ctx, job.Name); err == nil {
			info.LastRun = &state.LastRun
			info.NextRun = &state.NextRun
		}
		infos = append(infos, info)
	}
	return infos
}

// JobInfo describes the status of a single job.
type JobInfo struct {
	Name     string     `json:"name"`
	Interval string     `json:"interval"`
	CronExpr string     `json:"cron_expr,omitempty"`
	Timezone string     `json:"timezone,omitempty"`
	Enabled  bool       `json:"enabled"`
	LastRun  *time.Time `json:"last_run,omitempty"`
	NextRun  *time.Time `json:"next_run,omitempty"`
}

func (s *Scheduler) tick(ctx context.Context) {
	now := time.Now()

	// Maintenance: reclaim stale tasks and delete old ones.
	if reclaimed, err := s.store.CleanupStaleTasks(ctx, s.staleTimeout); err != nil {
		s.logger.Error("stale task cleanup failed", zap.Error(err))
	} else if reclaimed > 0 {
		s.logger.Info("reclaimed stale tasks", zap.Int("count", reclaimed))
	}

	if deleted, err := s.store.DeleteOldTasks(ctx, s.taskTTL); err != nil {
		s.logger.Error("old task deletion failed", zap.Error(err))
	} else if deleted > 0 {
		s.logger.Info("deleted old tasks", zap.Int("count", deleted))
	}

	// Check each job.
	for _, job := range s.jobs {
		if !job.Enabled {
			continue
		}

		state, err := s.store.GetSchedulerState(ctx, job.Name)
		if err != nil {
			s.logger.Error("failed to get job state", zap.String("job", job.Name), zap.Error(err))
			continue
		}

		if now.Before(state.NextRun) {
			continue // not due yet
		}

		// Job is due — create tasks.
		tasks := job.CreateTasks(ctx)
		created := 0
		for i := range tasks {
			ok, err := s.store.CreateTaskIfNotExists(ctx, &tasks[i])
			if err != nil {
				s.logger.Error("failed to create task",
					zap.String("job", job.Name), zap.Error(err))
				continue
			}
			if ok {
				created++
			}
		}

		// Update state.
		state.LastRun = now
		state.NextRun = s.computeNextRun(job, now)
		if err := s.store.SaveSchedulerState(ctx, state); err != nil {
			s.logger.Error("failed to save job state", zap.String("job", job.Name), zap.Error(err))
		}

		if created > 0 {
			s.logger.Info("job produced tasks",
				zap.String("job", job.Name),
				zap.Int("tasks_created", created))
		}
	}
}

// computeNextRun calculates the next run time for a job. If CronExpr is set,
// it parses the cron expression and computes the next time; otherwise it falls
// back to adding the Interval to now.
func (s *Scheduler) computeNextRun(job JobDefinition, now time.Time) time.Time {
	if job.CronExpr != "" {
		loc := time.UTC
		if job.Timezone != "" {
			if tz, err := time.LoadLocation(job.Timezone); err == nil {
				loc = tz
			} else {
				s.logger.Warn("invalid timezone for job, using UTC",
					zap.String("job", job.Name),
					zap.String("timezone", job.Timezone),
					zap.Error(err))
			}
		}
		schedule, err := cron.ParseStandard(job.CronExpr)
		if err != nil {
			s.logger.Warn("invalid cron expression, falling back to interval",
				zap.String("job", job.Name),
				zap.String("cron", job.CronExpr),
				zap.Error(err))
			return now.Add(job.Interval)
		}
		return schedule.Next(now.In(loc)).UTC()
	}
	return now.Add(job.Interval)
}

func (s *Scheduler) initJobState(ctx context.Context, job JobDefinition) error {
	_, err := s.store.GetSchedulerState(ctx, job.Name)
	if err == nil {
		return nil // already exists
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	// Create initial state. NextRun in 1 minute to allow startup warm-up.
	return s.store.SaveSchedulerState(ctx, &domain.SchedulerState{
		Name:    job.Name,
		NextRun: time.Now().Add(1 * time.Minute),
		Enabled: job.Enabled,
	})
}

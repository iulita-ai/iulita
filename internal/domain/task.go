package domain

import "time"

// TaskStatus represents the lifecycle state of a task.
type TaskStatus string

const (
	TaskStatusPending TaskStatus = "pending"
	TaskStatusClaimed TaskStatus = "claimed"
	TaskStatusRunning TaskStatus = "running"
	TaskStatusDone    TaskStatus = "done"
	TaskStatusFailed  TaskStatus = "failed"
)

// Task represents a unit of work in the distributed task scheduler.
type Task struct {
	ID           int64      `bun:",pk,autoincrement" json:"id"`
	Type         string     `bun:",notnull" json:"type"`                 // e.g. "insight.generate", "techfact.analyze"
	Payload      string     `bun:",notnull,default:'{}'" json:"payload"` // JSON input for the handler
	Status       TaskStatus `bun:",notnull,default:'pending'" json:"status"`
	Priority     int        `bun:",notnull,default:0" json:"priority"`      // higher = more urgent
	Capabilities string     `bun:",notnull,default:''" json:"capabilities"` // comma-separated required caps
	UniqueKey    string     `bun:",notnull,default:''" json:"unique_key"`   // dedup key, empty = no dedup

	ScheduledAt time.Time  `bun:",notnull" json:"scheduled_at"`
	ClaimedAt   *time.Time `bun:",nullzero" json:"claimed_at,omitempty"`
	StartedAt   *time.Time `bun:",nullzero" json:"started_at,omitempty"`
	FinishedAt  *time.Time `bun:",nullzero" json:"finished_at,omitempty"`

	WorkerID    string `bun:",notnull,default:''" json:"worker_id"`
	Attempts    int    `bun:",notnull,default:0" json:"attempts"`
	MaxAttempts int    `bun:",notnull,default:3" json:"max_attempts"`
	Error       string `bun:",notnull,default:''" json:"error"`
	Result      string `bun:",notnull,default:''" json:"result"`

	OneShot        bool `bun:",notnull,default:false" json:"one_shot"`
	DeleteAfterRun bool `bun:",notnull,default:false" json:"delete_after_run"`

	CreatedAt time.Time `bun:",nullzero,notnull,default:current_timestamp" json:"created_at"`
}

// SchedulerState persists scheduler metadata between restarts.
type SchedulerState struct {
	Name    string    `bun:",pk" json:"name"`
	LastRun time.Time `bun:",nullzero" json:"last_run"`
	NextRun time.Time `bun:",nullzero" json:"next_run"`
	Enabled bool      `bun:",notnull,default:true" json:"enabled"`
}

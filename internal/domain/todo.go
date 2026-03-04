package domain

import "time"

// TodoItem represents a user-facing task, either from the built-in manager
// or synced from an external provider (Todoist, Google Tasks, Craft).
type TodoItem struct {
	ID          int64      `bun:",pk,autoincrement" json:"id"`
	UserID      string     `bun:",notnull,default:''" json:"user_id"`
	Provider    string     `bun:",notnull,default:'builtin'" json:"provider"` // "builtin", "todoist", "google_tasks", "craft"
	ExternalID  string     `bun:",notnull,default:''" json:"external_id"`     // ID in external system
	Title       string     `bun:",notnull" json:"title"`
	Notes       string     `bun:",notnull,default:''" json:"notes"`
	DueDate     *time.Time `bun:",nullzero" json:"due_date,omitempty"`
	CompletedAt *time.Time `bun:",nullzero" json:"completed_at,omitempty"`
	Priority    int        `bun:",notnull,default:0" json:"priority"` // 0=none, 1=low, 2=medium, 3=high
	Labels      string     `bun:",notnull,default:''" json:"labels"`  // comma-separated
	ProjectName string     `bun:",notnull,default:''" json:"project_name"`
	URL         string     `bun:",notnull,default:''" json:"url"` // deep link for external tasks
	SyncedAt    *time.Time `bun:",nullzero" json:"synced_at,omitempty"`
	CreatedAt   time.Time  `bun:",nullzero,notnull,default:current_timestamp" json:"created_at"`
	UpdatedAt   time.Time  `bun:",nullzero,notnull,default:current_timestamp" json:"updated_at"`
}

// IsOverdue returns true if the task is not completed and due before the given date.
func (t *TodoItem) IsOverdue(now time.Time) bool {
	if t.CompletedAt != nil || t.DueDate == nil {
		return false
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return t.DueDate.Before(today)
}

// IsDueOn returns true if the task is due on the given date (date-only comparison).
func (t *TodoItem) IsDueOn(date time.Time) bool {
	if t.DueDate == nil {
		return false
	}
	y1, m1, d1 := t.DueDate.Date()
	y2, m2, d2 := date.Date()
	return y1 == y2 && m1 == m2 && d1 == d2
}

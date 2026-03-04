package dashboard

import (
	"context"

	"github.com/iulita-ai/iulita/internal/domain"
)

// TodoProvider syncs tasks from an external source into the local todo_items table.
type TodoProvider interface {
	// ProviderID returns the unique provider identifier (e.g. "todoist", "google_tasks", "craft").
	ProviderID() string
	// ProviderName returns a human-readable name for the provider.
	ProviderName() string
	// IsAvailable returns true if the provider is configured and ready.
	IsAvailable() bool
	// FetchAll retrieves all active (incomplete) tasks from the external source.
	// These will be upserted into todo_items by the sync handler.
	FetchAll(ctx context.Context, userID string) ([]domain.TodoItem, error)
	// CreateTask creates a task in the external system and returns the synced local item.
	CreateTask(ctx context.Context, userID string, req CreateTodoRequest) (*domain.TodoItem, error)
	// CompleteTask marks a task as done in the external system.
	CompleteTask(ctx context.Context, externalID string) error
}

// CreateTodoRequest is a request to create a new task.
type CreateTodoRequest struct {
	Title    string `json:"title"`
	Notes    string `json:"notes"`
	DueDate  string `json:"due_date"` // YYYY-MM-DD
	Priority int    `json:"priority"` // 0-3
	Provider string `json:"provider"` // target provider
}

// TodoProviderInfo describes a provider for the frontend.
type TodoProviderInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Available bool   `json:"available"`
	IsDefault bool   `json:"is_default"`
}

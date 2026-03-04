package dashboard

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/skill/todoist"
	"go.uber.org/zap"
)

// TodoistTaskClient is the subset of todoist.Client needed by the provider.
type TodoistTaskClient interface {
	GetTasksByFilter(ctx context.Context, filter string) ([]todoist.Task, error)
	CreateTask(ctx context.Context, params todoist.CreateTaskParams) (*todoist.Task, error)
	CloseTask(ctx context.Context, id string) error
}

// TodoistTokenChecker checks if the todoist API token is configured.
type TodoistTokenChecker interface {
	HasToken() bool
}

// TodoistProvider implements TodoProvider for Todoist.
type TodoistProvider struct {
	client       TodoistTaskClient
	tokenChecker TodoistTokenChecker
	logger       *zap.Logger
}

// NewTodoistProvider creates a new Todoist todo provider.
func NewTodoistProvider(client TodoistTaskClient, tokenChecker TodoistTokenChecker, logger *zap.Logger) *TodoistProvider {
	return &TodoistProvider{
		client:       client,
		tokenChecker: tokenChecker,
		logger:       logger,
	}
}

func (p *TodoistProvider) ProviderID() string   { return "todoist" }
func (p *TodoistProvider) ProviderName() string { return "Todoist" }
func (p *TodoistProvider) IsAvailable() bool    { return p.tokenChecker.HasToken() }

func (p *TodoistProvider) FetchAll(ctx context.Context, _ string) ([]domain.TodoItem, error) {
	tasks, err := p.client.GetTasksByFilter(ctx, "all")
	if err != nil {
		return nil, fmt.Errorf("todoist fetch: %w", err)
	}

	var items []domain.TodoItem
	for _, t := range tasks {
		items = append(items, todoistTaskToTodoItem(t))
	}
	return items, nil
}

func (p *TodoistProvider) CreateTask(ctx context.Context, _ string, req CreateTodoRequest) (*domain.TodoItem, error) {
	params := todoist.CreateTaskParams{
		Content: req.Title,
	}
	if req.Notes != "" {
		params.Description = req.Notes
	}
	if req.DueDate != "" {
		params.DueDate = req.DueDate
	}
	if req.Priority > 0 {
		// Map our priority (1=low, 2=medium, 3=high) to Todoist (4=urgent, 3=high, 2=medium, 1=normal).
		params.Priority = req.Priority + 1
		if params.Priority > 4 {
			params.Priority = 4
		}
	}

	task, err := p.client.CreateTask(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("todoist create: %w", err)
	}

	item := todoistTaskToTodoItem(*task)
	return &item, nil
}

func (p *TodoistProvider) CompleteTask(ctx context.Context, externalID string) error {
	return p.client.CloseTask(ctx, externalID)
}

func todoistTaskToTodoItem(t todoist.Task) domain.TodoItem {
	now := time.Now()
	item := domain.TodoItem{
		Provider:   "todoist",
		ExternalID: t.ID,
		Title:      t.Content,
		Notes:      t.Description,
		Priority:   todoistPriorityToOurs(t.Priority),
		Labels:     strings.Join(t.Labels, ","),
		URL:        fmt.Sprintf("https://todoist.com/app/task/%s", t.ID),
		SyncedAt:   &now,
	}
	if t.Due != nil {
		if d, err := parseTodoistDate(t.Due.Date); err == nil {
			item.DueDate = &d
		}
	}
	if t.IsCompleted {
		item.CompletedAt = &now
	}
	return item
}

// todoistPriorityToOurs converts Todoist priority (1=normal, 4=urgent) to ours (0=none, 3=high).
func todoistPriorityToOurs(p int) int {
	switch p {
	case 4:
		return 3 // urgent → high
	case 3:
		return 2 // high → medium
	case 2:
		return 1 // medium → low
	default:
		return 0
	}
}

func parseTodoistDate(s string) (time.Time, error) {
	// Todoist dates are YYYY-MM-DD or RFC3339.
	if len(s) == 10 {
		return time.Parse("2006-01-02", s)
	}
	return time.Parse(time.RFC3339, s)
}

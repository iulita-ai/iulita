package todoist

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

const (
	defaultBaseURL  = "https://api.todoist.com/api/v1"
	maxResponseSize = 1 << 20 // 1 MB
)

// Client communicates with the Todoist Unified API v1.
type Client struct {
	mu           sync.RWMutex
	apiToken     string
	baseURL      string
	httpClient   *http.Client
	logger       *zap.Logger
	logTokenOnce atomic.Pointer[sync.Once]
}

// NewClient creates a new Todoist API client.
func NewClient(apiToken string, httpClient *http.Client, logger *zap.Logger) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	c := &Client{
		apiToken:   apiToken,
		baseURL:    defaultBaseURL,
		httpClient: httpClient,
		logger:     logger,
	}
	c.logTokenOnce.Store(&sync.Once{})
	return c
}

// UpdateToken updates the API token at runtime (hot-reload).
func (c *Client) UpdateToken(token string) {
	c.mu.Lock()
	c.apiToken = token
	c.mu.Unlock()

	prefix := token
	if len(token) > 8 {
		prefix = token[:8]
	}
	c.logger.Info("todoist token updated (hot-reload)",
		zap.String("token_prefix", prefix+"..."),
	)
	// Reset logTokenOnce so the next API call logs the new token.
	c.logTokenOnce.Store(&sync.Once{})
}

func (c *Client) getToken() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.apiToken
}

// HasToken returns true if the API token is set.
func (c *Client) HasToken() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.apiToken != ""
}

// --- Data types ---

// Project represents a Todoist project (API v1 field names).
type Project struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Color          string `json:"color"`
	ParentID       string `json:"parent_id,omitempty"`
	Order          int    `json:"child_order"`
	IsShared       bool   `json:"is_shared"`
	IsFavorite     bool   `json:"is_favorite"`
	IsInboxProject bool   `json:"inbox_project"`
	ViewStyle      string `json:"view_style"`
	IsArchived     bool   `json:"is_archived"`
}

// CreateProjectParams holds parameters for creating a project.
type CreateProjectParams struct {
	Name       string `json:"name"`
	Color      string `json:"color,omitempty"`
	ParentID   string `json:"parent_id,omitempty"`
	IsFavorite bool   `json:"is_favorite,omitempty"`
	ViewStyle  string `json:"view_style,omitempty"`
}

// UpdateProjectParams holds parameters for updating a project.
type UpdateProjectParams struct {
	Name       string `json:"name,omitempty"`
	Color      string `json:"color,omitempty"`
	IsFavorite *bool  `json:"is_favorite,omitempty"`
	ViewStyle  string `json:"view_style,omitempty"`
}

// Collaborator represents a project collaborator.
type Collaborator struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Due represents a task due date in Todoist.
type Due struct {
	Date        string `json:"date"`
	String      string `json:"string"`
	IsRecurring bool   `json:"is_recurring"`
	Datetime    string `json:"datetime,omitempty"`
	Timezone    string `json:"timezone,omitempty"`
}

// Deadline represents a hard deadline in Todoist.
type Deadline struct {
	Date   string `json:"date"`
	String string `json:"string"`
}

// Duration represents a task duration in Todoist.
type Duration struct {
	Amount int    `json:"amount"`
	Unit   string `json:"unit"` // "minute" or "day"
}

// Task represents a Todoist task (API v1 field names).
type Task struct {
	ID          string    `json:"id"`
	Content     string    `json:"content"`
	Description string    `json:"description"`
	NoteCount   int       `json:"note_count"`
	IsCompleted bool      `json:"checked"`
	Order       int       `json:"child_order"`
	Priority    int       `json:"priority"` // 1 (normal) to 4 (urgent); inverted from UI
	ProjectID   string    `json:"project_id"`
	SectionID   string    `json:"section_id,omitempty"`
	ParentID    string    `json:"parent_id,omitempty"`
	Labels      []string  `json:"labels"`
	UserID      string    `json:"user_id"`
	AddedAt     string    `json:"added_at"`
	AssigneeID  string    `json:"responsible_uid,omitempty"`
	AssignerID  string    `json:"assigned_by_uid,omitempty"`
	Due         *Due      `json:"due,omitempty"`
	Deadline    *Deadline `json:"deadline,omitempty"`
	Duration    *Duration `json:"duration,omitempty"`
}

// Section represents a Todoist section (API v1 field names).
type Section struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Order     int    `json:"section_order"`
	Name      string `json:"name"`
}

// CreateSectionParams holds parameters for creating a section.
type CreateSectionParams struct {
	Name      string `json:"name"`
	ProjectID string `json:"project_id"`
	Order     int    `json:"order,omitempty"`
}

// UpdateSectionParams holds parameters for updating a section.
type UpdateSectionParams struct {
	Name string `json:"name,omitempty"`
}

// Label represents a Todoist label.
type Label struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Color      string `json:"color"`
	Order      int    `json:"order"`
	IsFavorite bool   `json:"is_favorite"`
}

// CreateLabelParams holds parameters for creating a label.
type CreateLabelParams struct {
	Name       string `json:"name"`
	Color      string `json:"color,omitempty"`
	Order      int    `json:"order,omitempty"`
	IsFavorite bool   `json:"is_favorite,omitempty"`
}

// UpdateLabelParams holds parameters for updating a label.
type UpdateLabelParams struct {
	Name       string `json:"name,omitempty"`
	Color      string `json:"color,omitempty"`
	Order      int    `json:"order,omitempty"`
	IsFavorite *bool  `json:"is_favorite,omitempty"`
}

// Comment represents a Todoist comment (API v1 field names).
type Comment struct {
	ID        string `json:"id"`
	Content   string `json:"content"`
	PostedAt  string `json:"posted_at"`
	TaskID    string `json:"item_id,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
}

// CreateTaskParams holds parameters for creating a task.
type CreateTaskParams struct {
	Content      string   `json:"content"`
	Description  string   `json:"description,omitempty"`
	ProjectID    string   `json:"project_id,omitempty"`
	SectionID    string   `json:"section_id,omitempty"`
	ParentID     string   `json:"parent_id,omitempty"`
	Labels       []string `json:"labels,omitempty"`
	Priority     int      `json:"priority,omitempty"`
	DueString    string   `json:"due_string,omitempty"`
	DueDate      string   `json:"due_date,omitempty"`
	DueDatetime  string   `json:"due_datetime,omitempty"`
	DueLang      string   `json:"due_lang,omitempty"`
	DurationUnit string   `json:"duration_unit,omitempty"`
	Duration     int      `json:"duration,omitempty"`
}

// UpdateTaskParams holds parameters for updating a task.
type UpdateTaskParams struct {
	Content      string   `json:"content,omitempty"`
	Description  string   `json:"description,omitempty"`
	Labels       []string `json:"labels,omitempty"`
	Priority     int      `json:"priority,omitempty"`
	DueString    string   `json:"due_string,omitempty"`
	DueDate      string   `json:"due_date,omitempty"`
	DueDatetime  string   `json:"due_datetime,omitempty"`
	DueLang      string   `json:"due_lang,omitempty"`
	DeadlineDate string   `json:"deadline_date,omitempty"`
	AssigneeID   string   `json:"assignee_id,omitempty"`
	Duration     int      `json:"duration,omitempty"`
	DurationUnit string   `json:"duration_unit,omitempty"`
}

// MoveTaskParams holds parameters for moving a task to a different project/section/parent.
type MoveTaskParams struct {
	ProjectID string `json:"project_id,omitempty"`
	SectionID string `json:"section_id,omitempty"`
	ParentID  string `json:"parent_id,omitempty"`
}

// esc escapes a path segment for safe URL construction.
func esc(id string) string { return url.PathEscape(id) }

// --- HTTP helpers ---

func (c *Client) doJSON(ctx context.Context, method, path string, body any, result any) error {
	u := c.baseURL + path

	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, bodyReader)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	tok := c.getToken()
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	c.logTokenOnce.Load().Do(func() {
		prefix := tok
		if len(tok) > 8 {
			prefix = tok[:8]
		}
		c.logger.Info("todoist client activated",
			zap.String("token_prefix", prefix+"..."),
			zap.String("base_url", c.baseURL),
		)
	})

	start := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.logger.Debug("todoist http error",
			zap.String("method", method),
			zap.String("path", path),
			zap.Error(err),
		)
		return fmt.Errorf("http %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 204 {
		c.logger.Debug("todoist api call",
			zap.String("method", method),
			zap.String("path", path),
			zap.Int("status", 204),
			zap.Duration("duration", time.Since(start)),
		)
		return nil // No content — success
	}

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	c.logger.Debug("todoist api call",
		zap.String("method", method),
		zap.String("path", path),
		zap.Int("status", resp.StatusCode),
		zap.Duration("duration", time.Since(start)),
		zap.Int("response_bytes", len(respBody)),
	)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("todoist API %s %s: status %d: %s", method, path, resp.StatusCode, string(respBody))
	}

	if result != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}

	return nil
}

func (c *Client) doNoContent(ctx context.Context, method, path string) error {
	u := c.baseURL + path

	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.getToken())

	start := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.logger.Debug("todoist http error",
			zap.String("method", method),
			zap.String("path", path),
			zap.Error(err),
		)
		return fmt.Errorf("http %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	c.logger.Debug("todoist api call",
		zap.String("method", method),
		zap.String("path", path),
		zap.Int("status", resp.StatusCode),
		zap.Duration("duration", time.Since(start)),
	)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
		return fmt.Errorf("todoist API %s %s: status %d: %s", method, path, resp.StatusCode, string(respBody))
	}

	return nil
}

// --- Projects ---

// GetProjects returns all projects.
func (c *Client) GetProjects(ctx context.Context) ([]Project, error) {
	var resp paginatedResponse[Project]
	if err := c.doJSON(ctx, http.MethodGet, "/projects", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Results, nil
}

// GetProject returns a single project by ID.
func (c *Client) GetProject(ctx context.Context, id string) (*Project, error) {
	var project Project
	if err := c.doJSON(ctx, http.MethodGet, "/projects/"+esc(id), nil, &project); err != nil {
		return nil, err
	}
	return &project, nil
}

// CreateProject creates a new project.
func (c *Client) CreateProject(ctx context.Context, params CreateProjectParams) (*Project, error) {
	var project Project
	if err := c.doJSON(ctx, http.MethodPost, "/projects", params, &project); err != nil {
		return nil, err
	}
	return &project, nil
}

// UpdateProject updates an existing project.
func (c *Client) UpdateProject(ctx context.Context, id string, params UpdateProjectParams) (*Project, error) {
	var project Project
	if err := c.doJSON(ctx, http.MethodPost, "/projects/"+esc(id), params, &project); err != nil {
		return nil, err
	}
	return &project, nil
}

// DeleteProject permanently deletes a project.
func (c *Client) DeleteProject(ctx context.Context, id string) error {
	return c.doNoContent(ctx, http.MethodDelete, "/projects/"+esc(id))
}

// ArchiveProject archives a project.
func (c *Client) ArchiveProject(ctx context.Context, id string) error {
	return c.doNoContent(ctx, http.MethodPost, "/projects/"+esc(id)+"/archive")
}

// UnarchiveProject unarchives a project.
func (c *Client) UnarchiveProject(ctx context.Context, id string) error {
	return c.doNoContent(ctx, http.MethodPost, "/projects/"+esc(id)+"/unarchive")
}

// GetArchivedProjects returns all archived projects.
func (c *Client) GetArchivedProjects(ctx context.Context) ([]Project, error) {
	var resp paginatedResponse[Project]
	if err := c.doJSON(ctx, http.MethodGet, "/projects/archived", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Results, nil
}

// GetProjectCollaborators returns collaborators of a project.
func (c *Client) GetProjectCollaborators(ctx context.Context, id string) ([]Collaborator, error) {
	var resp paginatedResponse[Collaborator]
	if err := c.doJSON(ctx, http.MethodGet, "/projects/"+esc(id)+"/collaborators", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Results, nil
}

// --- Tasks ---

// GetTasks returns tasks, optionally filtered by project, section, or label.
// Note: API v1's GET /tasks does NOT support the filter query param.
// Use GetTasksByFilter for filter queries.
func (c *Client) GetTasks(ctx context.Context, projectID, sectionID, label string) ([]Task, error) {
	params := url.Values{}
	if projectID != "" {
		params.Set("project_id", projectID)
	}
	if sectionID != "" {
		params.Set("section_id", sectionID)
	}
	if label != "" {
		params.Set("label", label)
	}

	path := "/tasks"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}

	var resp paginatedResponse[Task]
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Results, nil
}

// GetTask returns a single task by ID.
func (c *Client) GetTask(ctx context.Context, id string) (*Task, error) {
	var task Task
	if err := c.doJSON(ctx, http.MethodGet, "/tasks/"+esc(id), nil, &task); err != nil {
		return nil, err
	}
	return &task, nil
}

// CreateTask creates a new task.
func (c *Client) CreateTask(ctx context.Context, params CreateTaskParams) (*Task, error) {
	var task Task
	if err := c.doJSON(ctx, http.MethodPost, "/tasks", params, &task); err != nil {
		return nil, err
	}
	return &task, nil
}

// UpdateTask updates an existing task.
func (c *Client) UpdateTask(ctx context.Context, id string, params UpdateTaskParams) (*Task, error) {
	var task Task
	if err := c.doJSON(ctx, http.MethodPost, "/tasks/"+esc(id), params, &task); err != nil {
		return nil, err
	}
	return &task, nil
}

// CloseTask marks a task as completed. For recurring tasks, advances to next occurrence.
func (c *Client) CloseTask(ctx context.Context, id string) error {
	return c.doNoContent(ctx, http.MethodPost, "/tasks/"+esc(id)+"/close")
}

// ReopenTask reopens a completed task.
func (c *Client) ReopenTask(ctx context.Context, id string) error {
	return c.doNoContent(ctx, http.MethodPost, "/tasks/"+esc(id)+"/reopen")
}

// DeleteTask permanently deletes a task.
func (c *Client) DeleteTask(ctx context.Context, id string) error {
	return c.doNoContent(ctx, http.MethodDelete, "/tasks/"+esc(id))
}

// MoveTask moves a task to a different project, section, or parent.
func (c *Client) MoveTask(ctx context.Context, id string, params MoveTaskParams) (*Task, error) {
	var task Task
	if err := c.doJSON(ctx, http.MethodPost, "/tasks/"+esc(id)+"/move", params, &task); err != nil {
		return nil, err
	}
	return &task, nil
}

// QuickAddTask creates a task using natural language processing.
func (c *Client) QuickAddTask(ctx context.Context, text string) (*Task, error) {
	body := map[string]string{"text": text}
	var task Task
	if err := c.doJSON(ctx, http.MethodPost, "/tasks/quick", body, &task); err != nil {
		return nil, err
	}
	return &task, nil
}

// paginatedResponse wraps v1 API paginated list responses ({results: [], next_cursor: ""}).
type paginatedResponse[T any] struct {
	Results    []T    `json:"results"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// completedTasksResponse wraps the paginated response from the completed tasks endpoint.
type completedTasksResponse struct {
	Items      []Task `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// GetCompletedTasks returns completed tasks within an optional date range.
func (c *Client) GetCompletedTasks(ctx context.Context, since, until string) ([]Task, error) {
	params := url.Values{}
	if since != "" {
		params.Set("since", since)
	}
	if until != "" {
		params.Set("until", until)
	}
	path := "/tasks/completed/by_completion_date"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}
	var resp completedTasksResponse
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

// GetTasksByFilter returns tasks matching a Todoist filter query.
func (c *Client) GetTasksByFilter(ctx context.Context, filter string) ([]Task, error) {
	params := url.Values{}
	params.Set("query", filter)
	var resp paginatedResponse[Task]
	if err := c.doJSON(ctx, http.MethodGet, "/tasks/filter?"+params.Encode(), nil, &resp); err != nil {
		return nil, err
	}
	return resp.Results, nil
}

// GetCompletedTasksByDueDate returns tasks completed by due date within an optional date range.
func (c *Client) GetCompletedTasksByDueDate(ctx context.Context, since, until string) ([]Task, error) {
	params := url.Values{}
	if since != "" {
		params.Set("since", since)
	}
	if until != "" {
		params.Set("until", until)
	}
	path := "/tasks/completed/by_due_date"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}
	var resp completedTasksResponse
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

// --- Sections ---

// GetSections returns all sections for a project.
func (c *Client) GetSections(ctx context.Context, projectID string) ([]Section, error) {
	path := "/sections"
	if projectID != "" {
		params := url.Values{}
		params.Set("project_id", projectID)
		path += "?" + params.Encode()
	}
	var resp paginatedResponse[Section]
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Results, nil
}

// GetSection returns a single section by ID.
func (c *Client) GetSection(ctx context.Context, id string) (*Section, error) {
	var section Section
	if err := c.doJSON(ctx, http.MethodGet, "/sections/"+esc(id), nil, &section); err != nil {
		return nil, err
	}
	return &section, nil
}

// CreateSection creates a new section.
func (c *Client) CreateSection(ctx context.Context, params CreateSectionParams) (*Section, error) {
	var section Section
	if err := c.doJSON(ctx, http.MethodPost, "/sections", params, &section); err != nil {
		return nil, err
	}
	return &section, nil
}

// UpdateSection updates an existing section.
func (c *Client) UpdateSection(ctx context.Context, id string, params UpdateSectionParams) (*Section, error) {
	var section Section
	if err := c.doJSON(ctx, http.MethodPost, "/sections/"+esc(id), params, &section); err != nil {
		return nil, err
	}
	return &section, nil
}

// DeleteSection permanently deletes a section.
func (c *Client) DeleteSection(ctx context.Context, id string) error {
	return c.doNoContent(ctx, http.MethodDelete, "/sections/"+esc(id))
}

// ArchiveSection archives a section.
func (c *Client) ArchiveSection(ctx context.Context, id string) error {
	return c.doNoContent(ctx, http.MethodPost, "/sections/"+esc(id)+"/archive")
}

// UnarchiveSection unarchives a section.
func (c *Client) UnarchiveSection(ctx context.Context, id string) error {
	return c.doNoContent(ctx, http.MethodPost, "/sections/"+esc(id)+"/unarchive")
}

// SearchSections searches for sections matching a query.
func (c *Client) SearchSections(ctx context.Context, query string) ([]Section, error) {
	params := url.Values{}
	params.Set("query", query)
	var resp paginatedResponse[Section]
	if err := c.doJSON(ctx, http.MethodGet, "/sections/search?"+params.Encode(), nil, &resp); err != nil {
		return nil, err
	}
	return resp.Results, nil
}

// --- Labels ---

// GetLabels returns all personal labels.
func (c *Client) GetLabels(ctx context.Context) ([]Label, error) {
	var resp paginatedResponse[Label]
	if err := c.doJSON(ctx, http.MethodGet, "/labels", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Results, nil
}

// GetLabel returns a single label by ID.
func (c *Client) GetLabel(ctx context.Context, id string) (*Label, error) {
	var label Label
	if err := c.doJSON(ctx, http.MethodGet, "/labels/"+esc(id), nil, &label); err != nil {
		return nil, err
	}
	return &label, nil
}

// CreateLabel creates a new personal label.
func (c *Client) CreateLabel(ctx context.Context, params CreateLabelParams) (*Label, error) {
	var label Label
	if err := c.doJSON(ctx, http.MethodPost, "/labels", params, &label); err != nil {
		return nil, err
	}
	return &label, nil
}

// UpdateLabel updates an existing label.
func (c *Client) UpdateLabel(ctx context.Context, id string, params UpdateLabelParams) (*Label, error) {
	var label Label
	if err := c.doJSON(ctx, http.MethodPost, "/labels/"+esc(id), params, &label); err != nil {
		return nil, err
	}
	return &label, nil
}

// DeleteLabel permanently deletes a label.
func (c *Client) DeleteLabel(ctx context.Context, id string) error {
	return c.doNoContent(ctx, http.MethodDelete, "/labels/"+esc(id))
}

// SearchLabels searches for labels matching a query.
func (c *Client) SearchLabels(ctx context.Context, query string) ([]Label, error) {
	params := url.Values{}
	params.Set("query", query)
	var resp paginatedResponse[Label]
	if err := c.doJSON(ctx, http.MethodGet, "/labels/search?"+params.Encode(), nil, &resp); err != nil {
		return nil, err
	}
	return resp.Results, nil
}

// --- Comments ---

// GetComments returns comments for a task.
func (c *Client) GetComments(ctx context.Context, taskID string) ([]Comment, error) {
	params := url.Values{}
	params.Set("task_id", taskID)
	var resp paginatedResponse[Comment]
	if err := c.doJSON(ctx, http.MethodGet, "/comments?"+params.Encode(), nil, &resp); err != nil {
		return nil, err
	}
	return resp.Results, nil
}

// CreateComment adds a comment to a task.
func (c *Client) CreateComment(ctx context.Context, taskID, content string) (*Comment, error) {
	body := map[string]string{
		"task_id": taskID,
		"content": content,
	}
	var comment Comment
	if err := c.doJSON(ctx, http.MethodPost, "/comments", body, &comment); err != nil {
		return nil, err
	}
	return &comment, nil
}

// GetComment returns a single comment by ID.
func (c *Client) GetComment(ctx context.Context, id string) (*Comment, error) {
	var comment Comment
	if err := c.doJSON(ctx, http.MethodGet, "/comments/"+esc(id), nil, &comment); err != nil {
		return nil, err
	}
	return &comment, nil
}

// UpdateComment updates an existing comment.
func (c *Client) UpdateComment(ctx context.Context, id, content string) (*Comment, error) {
	body := map[string]string{"content": content}
	var comment Comment
	if err := c.doJSON(ctx, http.MethodPost, "/comments/"+esc(id), body, &comment); err != nil {
		return nil, err
	}
	return &comment, nil
}

// DeleteComment permanently deletes a comment.
func (c *Client) DeleteComment(ctx context.Context, id string) error {
	return c.doNoContent(ctx, http.MethodDelete, "/comments/"+esc(id))
}

// priorityLabel converts API priority (1=normal, 4=urgent) to user-friendly label.
func priorityLabel(p int) string {
	switch p {
	case 4:
		return "P1" // Urgent
	case 3:
		return "P2" // High
	case 2:
		return "P3" // Medium
	default:
		return "P4" // Normal
	}
}

// ParsePriority converts user-friendly priority (P1-P4) to API priority (4-1).
func ParsePriority(s string) int {
	s = strings.TrimSpace(strings.ToUpper(s))
	switch s {
	case "P1", "1":
		return 4
	case "P2", "2":
		return 3
	case "P3", "3":
		return 2
	default:
		return 1
	}
}

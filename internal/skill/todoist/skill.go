package todoist

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
)

// Skill provides Todoist task management capabilities.
type Skill struct {
	client *Client
	logger *zap.Logger

	// Hot-reload support
	capAdder capabilityAdder
	cfgStore configReader
}

type capabilityAdder interface {
	AddCapability(cap string)
	RemoveCapability(cap string)
}

type configReader interface {
	GetEffective(key string) (string, bool)
}

// NewSkill creates a new Todoist skill.
func NewSkill(client *Client, logger *zap.Logger) *Skill {
	return &Skill{client: client, logger: logger}
}

func (s *Skill) Name() string { return "todoist" }

func (s *Skill) Description() string {
	return "Manage Todoist: full CRUD for tasks, projects, sections, labels, and comments. List/create/update/delete/archive projects and sections. Create/update/delete labels. Quick-add tasks via natural language. Filter tasks, view completed history by completion or due date. Supports priorities, due dates, deadlines, recurring tasks, subtasks, and collaboration."
}

func (s *Skill) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["list", "get", "create", "update", "complete", "reopen", "delete", "move", "quick_add", "completed", "completed_by_due_date", "filter", "projects", "create_project", "update_project", "delete_project", "archive_project", "unarchive_project", "archived_projects", "project_collaborators", "sections", "create_section", "update_section", "delete_section", "labels", "create_label", "update_label", "delete_label", "search_labels", "comments", "add_comment", "update_comment", "delete_comment"],
				"description": "Action to perform. Tasks: list, get, create, update, complete, reopen, delete, move, quick_add, completed, completed_by_due_date, filter. Projects: projects, create_project, update_project, delete_project, archive_project, unarchive_project, archived_projects, project_collaborators. Sections: sections, create_section, update_section, delete_section. Labels: labels, create_label, update_label, delete_label, search_labels. Comments: comments, add_comment, update_comment, delete_comment."
			},
			"task_id": {
				"type": "string",
				"description": "Task ID (required for get, update, complete, reopen, delete, move, comments, add_comment)"
			},
			"content": {
				"type": "string",
				"description": "Task title/content (required for create, optional for update) or comment text (for add_comment)"
			},
			"description": {
				"type": "string",
				"description": "Task description/notes (for create or update)"
			},
			"project_id": {
				"type": "string",
				"description": "Project ID (for task filtering, task assignment, sections listing, update/delete/archive project, project_collaborators)"
			},
			"section_id": {
				"type": "string",
				"description": "Section ID (for task filtering, task assignment, update_section, delete_section)"
			},
			"parent_id": {
				"type": "string",
				"description": "Parent task ID to create a subtask"
			},
			"labels": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Labels to assign (label names, not IDs). Non-existent labels are auto-created."
			},
			"priority": {
				"type": "string",
				"enum": ["P1", "P2", "P3", "P4"],
				"description": "Priority: P1 (urgent), P2 (high), P3 (medium), P4 (normal/default)"
			},
			"due_string": {
				"type": "string",
				"description": "Natural language due date: 'tomorrow', 'next Monday', 'every Friday', 'Jan 15 at 3pm'. Supports recurring tasks."
			},
			"due_date": {
				"type": "string",
				"description": "Due date in YYYY-MM-DD format (alternative to due_string)"
			},
			"filter": {
				"type": "string",
				"description": "Todoist filter query: 'today', 'overdue', 'p1', '#Work', '@label', 'due before: Jan 15', 'today | overdue'. Used by list and filter actions."
			},
			"label": {
				"type": "string",
				"description": "Filter tasks by label name (for list action)"
			},
			"due_datetime": {
				"type": "string",
				"description": "Due datetime in RFC3339 format, e.g. 2026-03-15T14:00:00Z (for create or update)"
			},
			"deadline_date": {
				"type": "string",
				"description": "Hard deadline date in YYYY-MM-DD format (distinct from due date)"
			},
			"target_project_id": {
				"type": "string",
				"description": "Destination project ID (for move action)"
			},
			"target_section_id": {
				"type": "string",
				"description": "Destination section ID (for move action)"
			},
			"target_parent_id": {
				"type": "string",
				"description": "Destination parent task ID (for move action)"
			},
			"quick_add_text": {
				"type": "string",
				"description": "Natural language text for quick_add action, e.g. 'Buy groceries tomorrow p1 #Shopping'"
			},
			"since": {
				"type": "string",
				"description": "Start date for completed/completed_by_due_date query (RFC3339 or YYYY-MM-DD)"
			},
			"until": {
				"type": "string",
				"description": "End date for completed/completed_by_due_date query (RFC3339 or YYYY-MM-DD)"
			},
			"project_name": {
				"type": "string",
				"description": "Project name (for create_project, update_project)"
			},
			"project_color": {
				"type": "string",
				"description": "Project color name, e.g. 'red', 'blue' (for create_project, update_project)"
			},
			"project_parent_id": {
				"type": "string",
				"description": "Parent project ID for nested project creation"
			},
			"is_favorite": {
				"type": "boolean",
				"description": "Mark as favorite (for create_project, update_project, create_label, update_label)"
			},
			"view_style": {
				"type": "string",
				"enum": ["list", "board"],
				"description": "Project view style (for create_project, update_project)"
			},
			"section_name": {
				"type": "string",
				"description": "Section name (for create_section, update_section)"
			},
			"label_id": {
				"type": "string",
				"description": "Label ID (for update_label, delete_label)"
			},
			"label_name": {
				"type": "string",
				"description": "Label name (for create_label, update_label)"
			},
			"label_color": {
				"type": "string",
				"description": "Label color name (for create_label, update_label)"
			},
			"comment_id": {
				"type": "string",
				"description": "Comment ID (for update_comment, delete_comment)"
			},
			"comment_content": {
				"type": "string",
				"description": "New content text (for update_comment)"
			},
			"query": {
				"type": "string",
				"description": "Search query string (for search_labels)"
			}
		},
		"required": ["action"]
	}`)
}

func (s *Skill) RequiredCapabilities() []string {
	return []string{"todoist"}
}

// SetReloader enables hot-reload for config changes.
func (s *Skill) SetReloader(capAdder capabilityAdder, cfgStore configReader) {
	s.capAdder = capAdder
	s.cfgStore = cfgStore
}

// OnConfigChanged reacts to runtime config updates.
func (s *Skill) OnConfigChanged(key, value string) {
	if s.cfgStore == nil {
		return
	}

	s.logger.Debug("todoist config changed", zap.String("key", key))

	token, _ := s.cfgStore.GetEffective("skills.todoist.api_token")
	if token != "" {
		s.client.UpdateToken(token)
		if s.capAdder != nil {
			s.capAdder.AddCapability("todoist")
		}
		s.logger.Info("todoist skill activated via config reload")
	} else {
		if s.capAdder != nil {
			s.capAdder.RemoveCapability("todoist")
		}
		s.logger.Info("todoist skill deactivated (no token)")
	}
}

type todoistInput struct {
	Action          string   `json:"action"`
	TaskID          string   `json:"task_id"`
	Content         string   `json:"content"`
	Description     string   `json:"description"`
	ProjectID       string   `json:"project_id"`
	SectionID       string   `json:"section_id"`
	ParentID        string   `json:"parent_id"`
	Labels          []string `json:"labels"`
	Priority        string   `json:"priority"`
	DueString       string   `json:"due_string"`
	DueDate         string   `json:"due_date"`
	DueDatetime     string   `json:"due_datetime"`
	DeadlineDate    string   `json:"deadline_date"`
	Filter          string   `json:"filter"`
	Label           string   `json:"label"`
	TargetProjectID string   `json:"target_project_id"`
	TargetSectionID string   `json:"target_section_id"`
	TargetParentID  string   `json:"target_parent_id"`
	QuickAddText    string   `json:"quick_add_text"`
	Since           string   `json:"since"`
	Until           string   `json:"until"`

	// Project management
	ProjectName     string `json:"project_name"`
	ProjectColor    string `json:"project_color"`
	ProjectParentID string `json:"project_parent_id"`
	IsFavorite      *bool  `json:"is_favorite"`
	ViewStyle       string `json:"view_style"`

	// Section management
	SectionName string `json:"section_name"`

	// Label management
	LabelID    string `json:"label_id"`
	LabelName  string `json:"label_name"`
	LabelColor string `json:"label_color"`

	// Comment management
	CommentID      string `json:"comment_id"`
	CommentContent string `json:"comment_content"`

	// Search
	Query string `json:"query"`
}

func (s *Skill) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in todoistInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	s.logger.Debug("todoist skill dispatch", zap.String("action", in.Action))

	result, err := s.dispatch(ctx, in)
	if err != nil {
		s.logger.Debug("todoist skill error",
			zap.String("action", in.Action),
			zap.Error(err),
		)
		return result, err
	}

	s.logger.Debug("todoist skill result",
		zap.String("action", in.Action),
		zap.Int("result_len", len(result)),
	)
	return result, nil
}

func (s *Skill) dispatch(ctx context.Context, in todoistInput) (string, error) {
	switch in.Action {
	// Tasks
	case "list":
		return s.listTasks(ctx, in)
	case "get":
		return s.getTask(ctx, in.TaskID)
	case "create":
		return s.createTask(ctx, in)
	case "update":
		return s.updateTask(ctx, in)
	case "complete":
		return s.completeTask(ctx, in.TaskID)
	case "reopen":
		return s.reopenTask(ctx, in.TaskID)
	case "delete":
		return s.deleteTask(ctx, in.TaskID)
	case "move":
		return s.moveTask(ctx, in)
	case "quick_add":
		return s.quickAddTask(ctx, in)
	case "completed":
		return s.listCompletedTasks(ctx, in)
	case "completed_by_due_date":
		return s.listCompletedByDueDate(ctx, in)
	case "filter":
		return s.filterTasks(ctx, in.Filter)
	// Projects
	case "projects":
		return s.listProjects(ctx)
	case "create_project":
		return s.createProject(ctx, in)
	case "update_project":
		return s.updateProject(ctx, in)
	case "delete_project":
		return s.deleteProject(ctx, in.ProjectID)
	case "archive_project":
		return s.archiveProject(ctx, in.ProjectID)
	case "unarchive_project":
		return s.unarchiveProject(ctx, in.ProjectID)
	case "archived_projects":
		return s.listArchivedProjects(ctx)
	case "project_collaborators":
		return s.listProjectCollaborators(ctx, in.ProjectID)
	// Sections
	case "sections":
		return s.listSections(ctx, in.ProjectID)
	case "create_section":
		return s.createSection(ctx, in)
	case "update_section":
		return s.updateSection(ctx, in)
	case "delete_section":
		return s.deleteSection(ctx, in.SectionID)
	// Labels
	case "labels":
		return s.listLabels(ctx)
	case "create_label":
		return s.createLabel(ctx, in)
	case "update_label":
		return s.updateLabel(ctx, in)
	case "delete_label":
		return s.deleteLabel(ctx, in.LabelID)
	case "search_labels":
		return s.searchLabels(ctx, in.Query)
	// Comments
	case "comments":
		return s.listComments(ctx, in.TaskID)
	case "add_comment":
		return s.addComment(ctx, in.TaskID, in.Content)
	case "update_comment":
		return s.updateComment(ctx, in.CommentID, in.CommentContent)
	case "delete_comment":
		return s.deleteComment(ctx, in.CommentID)
	default:
		return "", fmt.Errorf("unknown action %q; see InputSchema for the full list of actions", in.Action)
	}
}

func (s *Skill) listTasks(ctx context.Context, in todoistInput) (string, error) {
	var tasks []Task
	var err error

	if in.Filter != "" {
		// API v1 requires the dedicated /tasks/filter endpoint for filter queries.
		// GET /tasks only supports project_id, section_id, and label params.
		tasks, err = s.client.GetTasksByFilter(ctx, in.Filter)
	} else {
		tasks, err = s.client.GetTasks(ctx, in.ProjectID, in.SectionID, in.Label)
	}
	if err != nil {
		return "", fmt.Errorf("listing tasks: %w", err)
	}

	if len(tasks) == 0 {
		hint := "No tasks found."
		if in.Filter != "" {
			hint = fmt.Sprintf("No tasks match filter %q.", in.Filter)
		}
		return hint, nil
	}

	now := time.Now()
	var b strings.Builder
	fmt.Fprintf(&b, "Tasks (%d):\n\n", len(tasks))

	for i, t := range tasks {
		s.formatTaskLine(&b, i+1, t, now)
	}
	return b.String(), nil
}

func (s *Skill) getTask(ctx context.Context, taskID string) (string, error) {
	if taskID == "" {
		return "", fmt.Errorf("task_id is required for get action")
	}

	t, err := s.client.GetTask(ctx, taskID)
	if err != nil {
		return "", fmt.Errorf("getting task: %w", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Task: %s\n", t.Content)
	fmt.Fprintf(&b, "ID: %s\n", t.ID)
	fmt.Fprintf(&b, "Priority: %s\n", priorityLabel(t.Priority))
	fmt.Fprintf(&b, "Status: %s\n", statusLabel(t.IsCompleted))

	if t.Description != "" {
		fmt.Fprintf(&b, "Description: %s\n", t.Description)
	}
	if t.Due != nil {
		fmt.Fprintf(&b, "Due: %s", t.Due.String)
		if t.Due.IsRecurring {
			b.WriteString(" (recurring)")
		}
		if t.Due.Datetime != "" {
			fmt.Fprintf(&b, " [%s]", t.Due.Datetime)
		}
		b.WriteString("\n")

		if !t.IsCompleted {
			if overdue := isOverdue(t.Due, time.Now()); overdue {
				b.WriteString("**OVERDUE**\n")
			}
		}
	}
	if t.Deadline != nil {
		fmt.Fprintf(&b, "Deadline: %s\n", t.Deadline.Date)
	}
	if t.Duration != nil {
		fmt.Fprintf(&b, "Duration: %d %s(s)\n", t.Duration.Amount, t.Duration.Unit)
	}
	if len(t.Labels) > 0 {
		fmt.Fprintf(&b, "Labels: %s\n", strings.Join(t.Labels, ", "))
	}
	if t.ParentID != "" {
		fmt.Fprintf(&b, "Parent task: %s\n", t.ParentID)
	}
	if t.ProjectID != "" {
		fmt.Fprintf(&b, "Project ID: %s\n", t.ProjectID)
	}
	if t.SectionID != "" {
		fmt.Fprintf(&b, "Section ID: %s\n", t.SectionID)
	}
	if t.NoteCount > 0 {
		fmt.Fprintf(&b, "Comments: %d\n", t.NoteCount)
	}

	return b.String(), nil
}

func (s *Skill) createTask(ctx context.Context, in todoistInput) (string, error) {
	if in.Content == "" {
		return "", fmt.Errorf("content is required for create action")
	}

	params := CreateTaskParams{
		Content:     in.Content,
		Description: in.Description,
		ProjectID:   in.ProjectID,
		SectionID:   in.SectionID,
		ParentID:    in.ParentID,
		Labels:      in.Labels,
		DueString:   in.DueString,
		DueDate:     in.DueDate,
		DueDatetime: in.DueDatetime,
	}

	if in.Priority != "" {
		params.Priority = ParsePriority(in.Priority)
	}

	task, err := s.client.CreateTask(ctx, params)
	if err != nil {
		return "", fmt.Errorf("creating task: %w", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Task created: %s [id: %s]", task.Content, task.ID)
	if task.Due != nil {
		fmt.Fprintf(&b, " (due: %s)", task.Due.String)
	}
	if in.Priority != "" {
		fmt.Fprintf(&b, " [%s]", priorityLabel(task.Priority))
	}
	return b.String(), nil
}

func (s *Skill) updateTask(ctx context.Context, in todoistInput) (string, error) {
	if in.TaskID == "" {
		return "", fmt.Errorf("task_id is required for update action")
	}

	params := UpdateTaskParams{
		Content:      in.Content,
		Description:  in.Description,
		Labels:       in.Labels,
		DueString:    in.DueString,
		DueDate:      in.DueDate,
		DueDatetime:  in.DueDatetime,
		DeadlineDate: in.DeadlineDate,
	}

	if in.Priority != "" {
		params.Priority = ParsePriority(in.Priority)
	}

	task, err := s.client.UpdateTask(ctx, in.TaskID, params)
	if err != nil {
		return "", fmt.Errorf("updating task: %w", err)
	}

	return fmt.Sprintf("Task updated: %s [id: %s]", task.Content, task.ID), nil
}

func (s *Skill) completeTask(ctx context.Context, taskID string) (string, error) {
	if taskID == "" {
		return "", fmt.Errorf("task_id is required for complete action")
	}

	if err := s.client.CloseTask(ctx, taskID); err != nil {
		return "", fmt.Errorf("completing task: %w", err)
	}
	return fmt.Sprintf("Task %s marked as completed.", taskID), nil
}

func (s *Skill) reopenTask(ctx context.Context, taskID string) (string, error) {
	if taskID == "" {
		return "", fmt.Errorf("task_id is required for reopen action")
	}

	if err := s.client.ReopenTask(ctx, taskID); err != nil {
		return "", fmt.Errorf("reopening task: %w", err)
	}
	return fmt.Sprintf("Task %s reopened.", taskID), nil
}

func (s *Skill) deleteTask(ctx context.Context, taskID string) (string, error) {
	if taskID == "" {
		return "", fmt.Errorf("task_id is required for delete action")
	}

	if err := s.client.DeleteTask(ctx, taskID); err != nil {
		return "", fmt.Errorf("deleting task: %w", err)
	}
	return fmt.Sprintf("Task %s deleted.", taskID), nil
}

func (s *Skill) moveTask(ctx context.Context, in todoistInput) (string, error) {
	if in.TaskID == "" {
		return "", fmt.Errorf("task_id is required for move action")
	}
	params := MoveTaskParams{
		ProjectID: in.TargetProjectID,
		SectionID: in.TargetSectionID,
		ParentID:  in.TargetParentID,
	}
	if params.ProjectID == "" && params.SectionID == "" && params.ParentID == "" {
		return "", fmt.Errorf("move action requires one of: target_project_id, target_section_id, target_parent_id")
	}
	task, err := s.client.MoveTask(ctx, in.TaskID, params)
	if err != nil {
		return "", fmt.Errorf("moving task: %w", err)
	}
	return fmt.Sprintf("Task moved: %s [id: %s]", task.Content, task.ID), nil
}

func (s *Skill) quickAddTask(ctx context.Context, in todoistInput) (string, error) {
	if in.QuickAddText == "" {
		return "", fmt.Errorf("quick_add_text is required for quick_add action")
	}
	task, err := s.client.QuickAddTask(ctx, in.QuickAddText)
	if err != nil {
		return "", fmt.Errorf("quick add task: %w", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Task created via quick add: %s [id: %s]", task.Content, task.ID)
	if task.Due != nil {
		fmt.Fprintf(&b, " (due: %s)", task.Due.String)
	}
	return b.String(), nil
}

func (s *Skill) listCompletedTasks(ctx context.Context, in todoistInput) (string, error) {
	tasks, err := s.client.GetCompletedTasks(ctx, in.Since, in.Until)
	if err != nil {
		return "", fmt.Errorf("listing completed tasks: %w", err)
	}
	if len(tasks) == 0 {
		return "No completed tasks found.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Completed tasks (%d):\n\n", len(tasks))
	for i, t := range tasks {
		fmt.Fprintf(&b, "%d. %s [id: %s]\n", i+1, t.Content, t.ID)
	}
	return b.String(), nil
}

func (s *Skill) listProjects(ctx context.Context) (string, error) {
	projects, err := s.client.GetProjects(ctx)
	if err != nil {
		return "", fmt.Errorf("listing projects: %w", err)
	}

	if len(projects) == 0 {
		return "No projects found.", nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Projects (%d):\n\n", len(projects))
	for i, p := range projects {
		inbox := ""
		if p.IsInboxProject {
			inbox = " (Inbox)"
		}
		fmt.Fprintf(&b, "%d. %s%s [id: %s]\n", i+1, p.Name, inbox, p.ID)
	}
	return b.String(), nil
}

func (s *Skill) listLabels(ctx context.Context) (string, error) {
	labels, err := s.client.GetLabels(ctx)
	if err != nil {
		return "", fmt.Errorf("listing labels: %w", err)
	}

	if len(labels) == 0 {
		return "No labels found.", nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Labels (%d):\n\n", len(labels))
	for i, l := range labels {
		fav := ""
		if l.IsFavorite {
			fav = " (favorite)"
		}
		fmt.Fprintf(&b, "%d. %s%s [id: %s]\n", i+1, l.Name, fav, l.ID)
	}
	return b.String(), nil
}

func (s *Skill) listSections(ctx context.Context, projectID string) (string, error) {
	if projectID == "" {
		return "", fmt.Errorf("project_id is required for sections action")
	}

	sections, err := s.client.GetSections(ctx, projectID)
	if err != nil {
		return "", fmt.Errorf("listing sections: %w", err)
	}

	if len(sections) == 0 {
		return "No sections found in this project.", nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Sections (%d):\n\n", len(sections))
	for i, sec := range sections {
		fmt.Fprintf(&b, "%d. %s [id: %s]\n", i+1, sec.Name, sec.ID)
	}
	return b.String(), nil
}

func (s *Skill) listComments(ctx context.Context, taskID string) (string, error) {
	if taskID == "" {
		return "", fmt.Errorf("task_id is required for comments action")
	}

	comments, err := s.client.GetComments(ctx, taskID)
	if err != nil {
		return "", fmt.Errorf("listing comments: %w", err)
	}

	if len(comments) == 0 {
		return "No comments on this task.", nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Comments (%d):\n\n", len(comments))
	for i, c := range comments {
		fmt.Fprintf(&b, "%d. %s\n   Posted: %s [id: %s]\n", i+1, c.Content, c.PostedAt, c.ID)
	}
	return b.String(), nil
}

func (s *Skill) addComment(ctx context.Context, taskID, content string) (string, error) {
	if taskID == "" {
		return "", fmt.Errorf("task_id is required for add_comment action")
	}
	if content == "" {
		return "", fmt.Errorf("content is required for add_comment action")
	}

	comment, err := s.client.CreateComment(ctx, taskID, content)
	if err != nil {
		return "", fmt.Errorf("adding comment: %w", err)
	}
	return fmt.Sprintf("Comment added [id: %s].", comment.ID), nil
}

func (s *Skill) listCompletedByDueDate(ctx context.Context, in todoistInput) (string, error) {
	tasks, err := s.client.GetCompletedTasksByDueDate(ctx, in.Since, in.Until)
	if err != nil {
		return "", fmt.Errorf("listing completed tasks by due date: %w", err)
	}
	if len(tasks) == 0 {
		return "No completed tasks found (by due date).", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Completed tasks by due date (%d):\n\n", len(tasks))
	for i, t := range tasks {
		fmt.Fprintf(&b, "%d. %s [id: %s]\n", i+1, t.Content, t.ID)
	}
	return b.String(), nil
}

func (s *Skill) filterTasks(ctx context.Context, filter string) (string, error) {
	if filter == "" {
		return "", fmt.Errorf("filter is required for filter action")
	}
	tasks, err := s.client.GetTasksByFilter(ctx, filter)
	if err != nil {
		return "", fmt.Errorf("filtering tasks: %w", err)
	}
	if len(tasks) == 0 {
		return fmt.Sprintf("No tasks match filter %q.", filter), nil
	}
	now := time.Now()
	var b strings.Builder
	fmt.Fprintf(&b, "Tasks matching %q (%d):\n\n", filter, len(tasks))
	for i, t := range tasks {
		s.formatTaskLine(&b, i+1, t, now)
	}
	return b.String(), nil
}

// --- Project actions ---

func (s *Skill) createProject(ctx context.Context, in todoistInput) (string, error) {
	if in.ProjectName == "" {
		return "", fmt.Errorf("project_name is required for create_project action")
	}
	params := CreateProjectParams{
		Name:      in.ProjectName,
		Color:     in.ProjectColor,
		ParentID:  in.ProjectParentID,
		ViewStyle: in.ViewStyle,
	}
	if in.IsFavorite != nil && *in.IsFavorite {
		params.IsFavorite = true
	}
	project, err := s.client.CreateProject(ctx, params)
	if err != nil {
		return "", fmt.Errorf("creating project: %w", err)
	}
	return fmt.Sprintf("Project created: %s [id: %s]", project.Name, project.ID), nil
}

func (s *Skill) updateProject(ctx context.Context, in todoistInput) (string, error) {
	if in.ProjectID == "" {
		return "", fmt.Errorf("project_id is required for update_project action")
	}
	params := UpdateProjectParams{
		Name:       in.ProjectName,
		Color:      in.ProjectColor,
		IsFavorite: in.IsFavorite,
		ViewStyle:  in.ViewStyle,
	}
	project, err := s.client.UpdateProject(ctx, in.ProjectID, params)
	if err != nil {
		return "", fmt.Errorf("updating project: %w", err)
	}
	return fmt.Sprintf("Project updated: %s [id: %s]", project.Name, project.ID), nil
}

func (s *Skill) deleteProject(ctx context.Context, projectID string) (string, error) {
	if projectID == "" {
		return "", fmt.Errorf("project_id is required for delete_project action")
	}
	if err := s.client.DeleteProject(ctx, projectID); err != nil {
		return "", fmt.Errorf("deleting project: %w", err)
	}
	return fmt.Sprintf("Project %s deleted.", projectID), nil
}

func (s *Skill) archiveProject(ctx context.Context, projectID string) (string, error) {
	if projectID == "" {
		return "", fmt.Errorf("project_id is required for archive_project action")
	}
	if err := s.client.ArchiveProject(ctx, projectID); err != nil {
		return "", fmt.Errorf("archiving project: %w", err)
	}
	return fmt.Sprintf("Project %s archived.", projectID), nil
}

func (s *Skill) unarchiveProject(ctx context.Context, projectID string) (string, error) {
	if projectID == "" {
		return "", fmt.Errorf("project_id is required for unarchive_project action")
	}
	if err := s.client.UnarchiveProject(ctx, projectID); err != nil {
		return "", fmt.Errorf("unarchiving project: %w", err)
	}
	return fmt.Sprintf("Project %s unarchived.", projectID), nil
}

func (s *Skill) listArchivedProjects(ctx context.Context) (string, error) {
	projects, err := s.client.GetArchivedProjects(ctx)
	if err != nil {
		return "", fmt.Errorf("listing archived projects: %w", err)
	}
	if len(projects) == 0 {
		return "No archived projects.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Archived projects (%d):\n\n", len(projects))
	for i, p := range projects {
		fmt.Fprintf(&b, "%d. %s [id: %s]\n", i+1, p.Name, p.ID)
	}
	return b.String(), nil
}

func (s *Skill) listProjectCollaborators(ctx context.Context, projectID string) (string, error) {
	if projectID == "" {
		return "", fmt.Errorf("project_id is required for project_collaborators action")
	}
	collabs, err := s.client.GetProjectCollaborators(ctx, projectID)
	if err != nil {
		return "", fmt.Errorf("listing collaborators: %w", err)
	}
	if len(collabs) == 0 {
		return "No collaborators on this project.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Collaborators (%d):\n\n", len(collabs))
	for i, c := range collabs {
		fmt.Fprintf(&b, "%d. %s (%s) [id: %s]\n", i+1, c.Name, c.Email, c.ID)
	}
	return b.String(), nil
}

// --- Section actions ---

func (s *Skill) createSection(ctx context.Context, in todoistInput) (string, error) {
	if in.SectionName == "" {
		return "", fmt.Errorf("section_name is required for create_section action")
	}
	if in.ProjectID == "" {
		return "", fmt.Errorf("project_id is required for create_section action")
	}
	params := CreateSectionParams{
		Name:      in.SectionName,
		ProjectID: in.ProjectID,
	}
	section, err := s.client.CreateSection(ctx, params)
	if err != nil {
		return "", fmt.Errorf("creating section: %w", err)
	}
	return fmt.Sprintf("Section created: %s [id: %s]", section.Name, section.ID), nil
}

func (s *Skill) updateSection(ctx context.Context, in todoistInput) (string, error) {
	if in.SectionID == "" {
		return "", fmt.Errorf("section_id is required for update_section action")
	}
	if in.SectionName == "" {
		return "", fmt.Errorf("section_name is required for update_section action")
	}
	params := UpdateSectionParams{Name: in.SectionName}
	section, err := s.client.UpdateSection(ctx, in.SectionID, params)
	if err != nil {
		return "", fmt.Errorf("updating section: %w", err)
	}
	return fmt.Sprintf("Section updated: %s [id: %s]", section.Name, section.ID), nil
}

func (s *Skill) deleteSection(ctx context.Context, sectionID string) (string, error) {
	if sectionID == "" {
		return "", fmt.Errorf("section_id is required for delete_section action")
	}
	if err := s.client.DeleteSection(ctx, sectionID); err != nil {
		return "", fmt.Errorf("deleting section: %w", err)
	}
	return fmt.Sprintf("Section %s deleted.", sectionID), nil
}

// --- Label actions ---

func (s *Skill) createLabel(ctx context.Context, in todoistInput) (string, error) {
	if in.LabelName == "" {
		return "", fmt.Errorf("label_name is required for create_label action")
	}
	params := CreateLabelParams{
		Name:  in.LabelName,
		Color: in.LabelColor,
	}
	if in.IsFavorite != nil && *in.IsFavorite {
		params.IsFavorite = true
	}
	label, err := s.client.CreateLabel(ctx, params)
	if err != nil {
		return "", fmt.Errorf("creating label: %w", err)
	}
	return fmt.Sprintf("Label created: %s [id: %s]", label.Name, label.ID), nil
}

func (s *Skill) updateLabel(ctx context.Context, in todoistInput) (string, error) {
	if in.LabelID == "" {
		return "", fmt.Errorf("label_id is required for update_label action")
	}
	params := UpdateLabelParams{
		Name:       in.LabelName,
		Color:      in.LabelColor,
		IsFavorite: in.IsFavorite,
	}
	label, err := s.client.UpdateLabel(ctx, in.LabelID, params)
	if err != nil {
		return "", fmt.Errorf("updating label: %w", err)
	}
	return fmt.Sprintf("Label updated: %s [id: %s]", label.Name, label.ID), nil
}

func (s *Skill) deleteLabel(ctx context.Context, labelID string) (string, error) {
	if labelID == "" {
		return "", fmt.Errorf("label_id is required for delete_label action")
	}
	if err := s.client.DeleteLabel(ctx, labelID); err != nil {
		return "", fmt.Errorf("deleting label: %w", err)
	}
	return fmt.Sprintf("Label %s deleted.", labelID), nil
}

func (s *Skill) searchLabels(ctx context.Context, query string) (string, error) {
	if query == "" {
		return "", fmt.Errorf("query is required for search_labels action")
	}
	labels, err := s.client.SearchLabels(ctx, query)
	if err != nil {
		return "", fmt.Errorf("searching labels: %w", err)
	}
	if len(labels) == 0 {
		return fmt.Sprintf("No labels match query %q.", query), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Labels matching %q (%d):\n\n", query, len(labels))
	for i, l := range labels {
		fmt.Fprintf(&b, "%d. %s [id: %s]\n", i+1, l.Name, l.ID)
	}
	return b.String(), nil
}

// --- Comment actions ---

func (s *Skill) updateComment(ctx context.Context, commentID, content string) (string, error) {
	if commentID == "" {
		return "", fmt.Errorf("comment_id is required for update_comment action")
	}
	if content == "" {
		return "", fmt.Errorf("comment_content is required for update_comment action")
	}
	comment, err := s.client.UpdateComment(ctx, commentID, content)
	if err != nil {
		return "", fmt.Errorf("updating comment: %w", err)
	}
	return fmt.Sprintf("Comment updated [id: %s].", comment.ID), nil
}

func (s *Skill) deleteComment(ctx context.Context, commentID string) (string, error) {
	if commentID == "" {
		return "", fmt.Errorf("comment_id is required for delete_comment action")
	}
	if err := s.client.DeleteComment(ctx, commentID); err != nil {
		return "", fmt.Errorf("deleting comment: %w", err)
	}
	return fmt.Sprintf("Comment %s deleted.", commentID), nil
}

// --- Formatting helpers ---

func (s *Skill) formatTaskLine(b *strings.Builder, num int, t Task, now time.Time) {
	prio := ""
	if t.Priority > 1 {
		prio = " [" + priorityLabel(t.Priority) + "]"
	}

	overdue := ""
	if t.Due != nil && !t.IsCompleted {
		if isOverdue(t.Due, now) {
			overdue = " **OVERDUE**"
		}
	}

	fmt.Fprintf(b, "%d. %s%s%s\n", num, t.Content, prio, overdue)

	if t.Description != "" {
		desc := t.Description
		if len(desc) > 150 {
			desc = desc[:150] + "..."
		}
		fmt.Fprintf(b, "   %s\n", desc)
	}
	if t.Due != nil {
		fmt.Fprintf(b, "   Due: %s", t.Due.String)
		if t.Due.IsRecurring {
			b.WriteString(" (recurring)")
		}
		b.WriteString("\n")
	}
	if len(t.Labels) > 0 {
		fmt.Fprintf(b, "   Labels: %s\n", strings.Join(t.Labels, ", "))
	}
	fmt.Fprintf(b, "   [id: %s]\n", t.ID)
}

func statusLabel(completed bool) string {
	if completed {
		return "completed"
	}
	return "active"
}

func isOverdue(due *Due, now time.Time) bool {
	if due == nil {
		return false
	}
	if due.Datetime != "" {
		d, err := time.Parse(time.RFC3339, due.Datetime)
		if err == nil && d.Before(now) {
			return true
		}
	} else if due.Date != "" {
		d, err := time.Parse("2006-01-02", due.Date)
		if err == nil {
			// A date-only due is overdue after the end of that day.
			endOfDay := d.Add(24 * time.Hour)
			return now.After(endOfDay)
		}
	}
	return false
}

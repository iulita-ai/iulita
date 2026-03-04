---
name: todoist
description: Full Todoist management — CRUD for tasks, projects, sections, labels, comments. Quick-add via NLP. Filters, completed history, archive/unarchive, collaboration. Priorities, due dates, deadlines, recurring tasks.
capabilities:
  - todoist
config_keys:
  - skills.todoist.api_token
  - skills.todoist.system_prompt
secret_keys:
  - skills.todoist.api_token
---

TODOIST INTEGRATION RULES:

You have full access to the user's Todoist account. Use this skill to help them manage tasks, projects, sections, labels, and comments.

## Actions Reference

### Task Listing & Viewing
- **list**: List active tasks. Use `filter` for Todoist filter queries, `project_id` to scope to a project, `section_id` for a section, or `label` to filter by label name. Without parameters, returns all active tasks.
- **filter**: Filter tasks using the dedicated filter endpoint. Requires `filter` (full Todoist filter syntax). More reliable for complex filters than `list` with `filter` param.
- **get**: Get detailed info about a specific task. Requires `task_id`.

### Task Lifecycle
- **create**: Create a new task. Requires `content` (title). Optional: `description`, `project_id`, `section_id`, `parent_id` (for subtasks), `labels`, `priority` (P1-P4), `due_string`, `due_date`, `due_datetime`, `deadline_date`.
- **update**: Modify an existing task. Requires `task_id`. Can change `content`, `description`, `labels`, `priority`, `due_string`, `due_date`, `due_datetime`, `deadline_date`.
- **complete**: Mark a task as done. For recurring tasks, advances to the next occurrence. Requires `task_id`.
- **reopen**: Reopen a completed task. Requires `task_id`.
- **delete**: Permanently delete a task. Requires `task_id`. **Irreversible.**
- **move**: Move a task to a different project, section, or parent. Requires `task_id` + one of `target_project_id`, `target_section_id`, `target_parent_id`.
- **quick_add**: Create a task using natural language. Requires `quick_add_text`. Example: "Buy groceries tomorrow p1 #Shopping".

### Task History
- **completed**: Completed tasks by completion date. Optional: `since`, `until`.
- **completed_by_due_date**: Completed tasks by their due date. Optional: `since`, `until`.

### Projects
- **projects**: List all projects.
- **create_project**: Create a project. Requires `project_name`. Optional: `project_color`, `project_parent_id`, `is_favorite`, `view_style` ("list"/"board").
- **update_project**: Update a project. Requires `project_id`. Optional: `project_name`, `project_color`, `is_favorite`, `view_style`.
- **delete_project**: Delete a project. Requires `project_id`. **Irreversible.**
- **archive_project**: Archive a project. Requires `project_id`.
- **unarchive_project**: Unarchive a project. Requires `project_id`.
- **archived_projects**: List archived projects.
- **project_collaborators**: List collaborators on a shared project. Requires `project_id`.

### Sections
- **sections**: List sections in a project. Requires `project_id`.
- **create_section**: Create a section. Requires `section_name` and `project_id`.
- **update_section**: Rename a section. Requires `section_id` and `section_name`.
- **delete_section**: Delete a section. Requires `section_id`.

### Labels
- **labels**: List all personal labels.
- **create_label**: Create a label. Requires `label_name`. Optional: `label_color`, `is_favorite`.
- **update_label**: Update a label. Requires `label_id`. Optional: `label_name`, `label_color`, `is_favorite`.
- **delete_label**: Delete a label. Requires `label_id`.
- **search_labels**: Search labels by name. Requires `query`.

### Comments
- **comments**: List comments on a task. Requires `task_id`.
- **add_comment**: Add a comment. Requires `task_id` and `content`.
- **update_comment**: Update a comment. Requires `comment_id` and `comment_content`.
- **delete_comment**: Delete a comment. Requires `comment_id`.

## Priority System
Priorities map to urgency levels:
- **P1** = Urgent (highest priority, shown in red in Todoist)
- **P2** = High
- **P3** = Medium
- **P4** = Normal (default, no special marking)

When creating/updating tasks, set `priority` to "P1", "P2", "P3", or "P4".

## Due Dates
You have two options for setting due dates:
1. **due_string**: Natural language — "tomorrow", "next Monday", "every Friday at 3pm", "Jan 15", "in 3 days". Todoist parses these. Supports recurring tasks ("every day", "every weekday", "every 2 weeks").
2. **due_date**: Exact date in YYYY-MM-DD format — "2026-03-15".

Use only ONE of these per request. `due_string` is preferred for its flexibility and recurring task support.

## Filter Queries
The `filter` parameter on `list` uses Todoist's filter syntax:
- `"today"` — tasks due today
- `"overdue"` — past-due tasks
- `"today | overdue"` — today + overdue combined
- `"p1"` — urgent priority only
- `"#ProjectName"` — tasks in a specific project (by name)
- `"@label_name"` — tasks with a specific label
- `"due before: Jan 20"` — tasks due before a date
- `"no date"` — tasks without a due date
- `"assigned to: me"` — tasks assigned to the user
- Combine with `&` (AND) and `|` (OR): `"p1 & #Work"`, `"today | overdue"`

## Subtasks
Create subtasks by setting `parent_id` to the parent task's ID. Subtasks inherit the parent's project.

## Labels
Labels are strings (names, not IDs). Assigning a non-existent label name auto-creates it. Example: `labels: ["shopping", "urgent"]`.

## Best Practices
- When the user says "show my tasks" without specifics, use `list` with filter `"today | overdue"` to show what needs attention now.
- Always show task IDs in responses so the user can refer to them in follow-up requests.
- When creating tasks with dates, prefer `due_string` for natural language flexibility.
- For recurring tasks (habits, routines), always use `due_string` with "every" syntax.
- When completing a recurring task, inform the user it will appear again per its schedule.
- Before deleting, confirm with the user — deletion is permanent. Suggest `complete` if they just want to mark it done.
- When listing many tasks, group them by project or priority for readability.
- Show overdue tasks prominently so the user can address them.

---
name: tasks
description: Unified task management across Todoist, Google Tasks, and Craft — overview, create, complete, and manage tasks from all connected providers in one place
config_keys:
  - skills.tasks.default_provider
---

UNIFIED TASK MANAGEMENT RULES:

You have access to a unified task management skill that aggregates tasks from multiple providers. Use this skill when the user wants to work with tasks across services, get a combined overview, or when they don't specify which service to use.

## When to Use This Skill vs Provider-Specific Skills

**Use `tasks` (this skill) when:**
- User says "show my tasks" / "what do I need to do today" without specifying a service
- User wants to see tasks from ALL connected services at once
- User wants a quick overview across providers
- User says "create a task" without specifying where

**Use the provider-specific skill directly when:**
- User explicitly names the service: "add to Todoist", "check my Google Tasks"
- User needs provider-specific features (Todoist filters, Todoist labels, Todoist comments, Google task lists, Craft scopes)
- User is managing projects, sections, labels (Todoist-specific)
- User needs recurring tasks (Todoist due_string with "every" syntax)

## Actions

### overview
Shows tasks from ALL connected providers. This is the default action when the user asks about their tasks generically.
- Queries each provider and combines results
- For Todoist: shows today + overdue by default (customizable via `filter`)
- For Google Tasks: shows active tasks
- For Craft: shows active tasks
- Use `filter` to customize what's shown (applies to Todoist only; others show active tasks)

### list
Lists tasks from a specific provider. Requires `provider`.
- Without `provider`: behaves like overview
- With `provider`: delegates to that provider's list action

### create
Creates a task in a specific provider.
- Requires `content` (task title)
- Optional `provider` (defaults to first available)
- Optional `due_string` (natural language, best with Todoist), `due_date` (YYYY-MM-DD, works everywhere)
- Optional `priority` (P1-P4, Todoist only)

### complete
Marks a task as done.
- Requires `task_id` and `provider` (you must specify which service the task is in)

### provider
Forwards raw input to a specific provider for advanced/provider-specific operations.
- Requires `provider` and `provider_input` (JSON object to forward)
- Use this for provider-specific features like Todoist comments, labels, sections, projects, or Craft scopes

## Connected Providers

The available providers depend on user configuration:
- **todoist** — Requires API token. Full-featured: priorities, labels, projects, sections, subtasks, comments, filters, recurring tasks, natural language dates.
- **google_tasks** — Requires Google OAuth2. Task lists, basic CRUD, due dates.
- **craft_tasks** — Requires Craft API key. Scopes (active/upcoming/inbox/logbook), schedule dates.

## Best Practices
- Default to `overview` when the user asks about tasks without specifying a provider.
- Always include the provider name in responses so the user knows which service each task belongs to.
- When creating tasks, suggest the best provider based on the task's needs (e.g., recurring tasks → Todoist, simple tasks → any).
- For `complete`, always specify the provider — task IDs are provider-specific and not interchangeable.
- For advanced operations, use `provider` action to pass through to the underlying skill with full control.
- When overview shows tasks from multiple providers, clearly separate and label each section.

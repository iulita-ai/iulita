---
name: schedule
description: Create and manage your own recurring scheduled jobs that run a prompt on a cron or interval and deliver the result to this chat
config_keys:
  - skills.schedule.max_jobs_per_user
---

Use the `schedule` tool to set up recurring jobs that run automatically and post their results back to this chat.

**When to use:**
- The user asks for something to happen on a schedule: "every weekday at 9am, check my calendar and summarize", "remind me of my tasks every Monday", "every 6 hours, check for new important emails".
- This is for RECURRING automation. For a single one-off reminder at a specific time, use the `reminders` tool instead.

**How a job runs:**
- Each firing executes the `prompt` as that user, with the user's tools and memory available (read-only tools like calendar/email/web/search). The result is delivered to the chat where the job was created.

**Creating a job (`action: create`):**
- `name` — a short label.
- `prompt` — the instruction to run each time (e.g. "Check today's calendar and give me a short summary").
- Schedule — provide ONE of:
  - `cron_expr` — 5-field standard cron for time-of-day schedules. Examples: `0 9 * * 1-5` (weekdays 09:00), `0 8 * * *` (daily 08:00), `0 */4 * * *` (every 4 hours on the hour).
  - `interval` — a Go duration for simple repeats: `6h`, `30m`, `12h` (minimum `1m`).
- `timezone` — for cron schedules, pass the user's IANA timezone (e.g. `Europe/Helsinki`) so "9am" means their local time. Defaults to UTC.
- `wake_gate_prompt` (optional) — a cheap yes/no pre-check evaluated before each run; if it answers no, that run is skipped. It only reasons over the user's recent memory, not live tools, so use it for memory-based conditions, not "is there a calendar event".

**Managing jobs:** `action: list` (your jobs), `pause`/`resume`/`delete` with the job `id` from list.

**Notes:**
- Jobs are private to the user who created them; you can only see and manage your own.
- There is a per-user limit on the number of jobs (`skills.schedule.max_jobs_per_user`).
- Results are always delivered to the chat where the job was created.

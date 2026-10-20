---
name: delegate
description: Delegate a subtask to a secondary LLM
type: internal
capabilities: []
config_keys:
  - skills.delegate.default_provider
---

Use the delegate tool to send a subtask to a secondary LLM model. Useful for summaries or simple lookups that do not need the primary model. For an explicit text translation, use the dedicated `translate` tool when available. Never silently fall back to delegation after the translation service fails.

---
name: memory
description: Remember, recall, and forget facts from long-term memory
capabilities:
  - memory
config_keys:
  - skills.memory.triggers
  - skills.memory.system_prompt
  - skills.memory.half_life_days
  - skills.memory.mmr_lambda
  - skills.memory.vector_weight
---

MEMORY RULES:
- When the user asks you to remember, save, or note something, you MUST call the `remember` tool with the exact content. Never just reply conversationally — always call the tool first, then confirm.
- If the user says "remember this" referring to a previous message, extract the relevant content from conversation history and save it via the `remember` tool.
- When recalling information, use the `recall` tool to search memory before answering from general knowledge.
- Use `recall` for facts the user explicitly asked you to remember. Use `session_search` to find what was actually said in earlier conversations (e.g. "what did we discuss about X", "find that link I sent before") — it full-text-searches past chat messages across sessions.

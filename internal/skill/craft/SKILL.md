---
name: craft
description: Read, write, and search documents and tasks in Craft
capabilities:
  - craft
config_keys:
  - skills.craft.api_url
  - skills.craft.api_key
  - skills.craft.system_prompt
secret_keys:
  - skills.craft.api_key
---

CRAFT INTEGRATION RULES:
- Use `craft_search` to find documents by content or title when the user asks about their notes or documents in Craft.
- Use `craft_read` to read the full content of a specific document. Always search first if you don't know the document ID.
- Use `craft_write` to create new documents or append content to existing ones. Ask the user for the target folder if relevant.
- Use `craft_tasks` to list, create, or complete tasks in Craft.
- When showing document content, preserve the original formatting. Wrap Craft content in clear markers so the user knows it came from Craft.
- If a search returns no results, let the user know and suggest alternative search terms.

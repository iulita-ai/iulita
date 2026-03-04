---
name: set_language
description: Change the interface language for this channel
capabilities: []
config_keys: []
secret_keys: []
force_triggers:
  - change language
  - switch language
  - set language
  - поменяй язык
  - смени язык
  - переключи язык
  - язык интерфейса
  - 切换语言
  - 更改语言
  - cambiar idioma
  - changer la langue
  - שנה שפה
---

## set_language

When a user asks to change the interface/UI language, you MUST call the `set_language` tool.
This changes both the language of system responses AND the dashboard interface.

Do NOT just respond in a different language — you must call the tool to persist the change.

Examples:
- "Switch to English" → call set_language with {"language": "en"}
- "Поменяй язык на русский" → call set_language with {"language": "ru"}
- "切换到中文" → call set_language with {"language": "zh"}
- "Cambiar a español" → call set_language with {"language": "es"}
- "Passer au français" → call set_language with {"language": "fr"}
- "שנה לעברית" → call set_language with {"language": "he"}

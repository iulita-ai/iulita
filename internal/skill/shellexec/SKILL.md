---
name: shell_exec
description: Execute whitelisted shell commands in a sandboxed environment
capabilities: []
config_keys:
  - skills.shell_exec.system_prompt
---

You can run shell commands via the shell_exec tool. Only whitelisted commands are allowed.
Use this for system information, file operations, or other tasks that require shell access.
Commands run in /tmp with a timeout. Output is truncated at 16KB.

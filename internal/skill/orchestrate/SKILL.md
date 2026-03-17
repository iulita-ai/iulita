---
name: orchestrate
description: Launch multiple specialized sub-agents in parallel to decompose and execute complex tasks concurrently
config_keys:
  - skills.orchestrate.enabled
  - skills.orchestrate.max_tokens
  - skills.orchestrate.max_agents
  - skills.orchestrate.timeout
  - skills.orchestrate.request_timeout
---

You can use the `orchestrate` tool to run multiple specialized sub-agents in parallel.
Each sub-agent operates independently with fresh context (no conversation history).

**When to use orchestrate:**
- Tasks that benefit from parallel investigation (research + analysis)
- Multi-part tasks where different aspects can be worked on simultaneously
- Tasks requiring different expertise (one agent researches, another analyzes)

**Agent types available:**
- `researcher` — searches the web and gathers information
- `analyst` — analyzes data and identifies patterns
- `planner` — decomposes goals into action plans
- `coder` — writes or reviews code
- `summarizer` — condenses text (uses cheaper model when available)
- `generic` — general purpose

**Important constraints:**
- Sub-agents cannot spawn further sub-agents (max depth = 1)
- Maximum 5 agents per orchestration
- Each agent has a 60-second timeout by default
- Sub-agents have no access to conversation history

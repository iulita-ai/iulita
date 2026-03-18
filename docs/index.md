---
layout: home
title: Iulita.ai — Personal AI Assistant
titleTemplate: false

hero:
  name: Iulita.ai
  text: Your AI assistant that remembers the truth
  tagline: Fact-based memory. Multi-channel. Runs locally. Open source.
  image:
    src: /logo.svg
    alt: Iulita.ai
  actions:
    - theme: brand
      text: Get Started
      link: /en/getting-started
    - theme: alt
      text: View on GitHub
      link: https://github.com/iulita-ai/iulita

features:
  - icon: "\U0001F9E0"
    title: Fact-Based Memory
    details: Stores only verified facts you explicitly share. Never hallucinated. Cross-referenced into insights across all your conversations.
  - icon: "\U0001F50D"
    title: Hybrid Search
    details: FTS5 full-text search combined with ONNX vector embeddings. MMR reranking ensures diverse, relevant recall every time.
  - icon: "\U0001F4AC"
    title: Multi-Channel
    details: Console TUI (default), Telegram bot, Web Chat. One identity across all channels — facts remembered anywhere are available everywhere.
  - icon: "\U0001F916"
    title: Smart Model Routing
    details: Auto-routes to cheap models (Haiku) for background tasks and synthesis. Skills declare their own cost tier. Sonnet for reasoning, Haiku for formatting. 40-60% cost savings.
  - icon: "\U0001F50C"
    title: 25+ Skills & Token Dashboard
    details: Web search, weather, Google Workspace, Todoist, Craft, multi-agent orchestration, token usage stats, and more. Per-model cost tracking with admin dashboard.
  - icon: "\U0001F5C4\uFE0F"
    title: Runs Locally
    details: SQLite with WAL mode, zero cloud dependencies for storage. XDG-compliant paths, keyring secret storage, zero-config local install.
  - icon: "\U0001F310"
    title: 6 Languages
    details: Full i18n in English, Russian, Chinese, Spanish, French, and Hebrew with RTL support. Per-channel locale switching.
  - icon: "\U0001F6E1\uFE0F"
    title: Privacy First
    details: Your data stays on your machine. No training on your conversations. SSRF protection, JWT auth, AES-256-GCM encryption for secrets.
---

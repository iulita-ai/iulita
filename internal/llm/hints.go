package llm

// RouteHintCheap is the semantic route for light/cheap tasks (background jobs,
// context compression, recall/summarizer synthesis). It maps to the provider
// configured via routing.light_provider (default: "claude-haiku"). If the
// "light" route is not registered (light routing disabled or provider missing),
// requests fall through to the default provider silently.
const RouteHintCheap = "light"

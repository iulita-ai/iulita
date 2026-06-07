package llm

// RouteHintCheap is the semantic route for light/cheap tasks (background jobs,
// context compression, recall/summarizer synthesis). It maps to the provider
// configured via routing.light_provider (default: "claude-haiku"). If the
// "light" route is not registered (light routing disabled or provider missing),
// requests fall through to the default provider silently.
const RouteHintCheap = "light"

// RouteHintVision routes a request that carries image attachments to a
// vision-capable provider (e.g. Claude). The default provider (e.g. DeepSeek)
// may not support images and would silently drop them. Falls through to the
// default provider if no "vision" route is registered.
const RouteHintVision = "vision"

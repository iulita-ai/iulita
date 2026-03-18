package llm

// RouteHintCheap routes to the cheapest available provider (claude-haiku when
// Claude is the primary provider). Falls back to the default provider silently
// if the hint is not registered in the RoutingProvider.
const RouteHintCheap = "claude-haiku"

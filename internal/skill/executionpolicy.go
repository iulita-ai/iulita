package skill

import "encoding/json"

// ExecutionPolicy is an optional interface for skills whose tool calls must
// not be treated as cacheable queries: repeated identical calls are separate
// operations (a re-issued POST is a new mutation; a re-issued GET may be an
// intentional re-check of a changed resource). It also replaces the default
// sensitive handling of inputs and outputs in the assistant loop.
type ExecutionPolicy interface {
	// SkipLedgerDedup excludes the skill from the exploration ledger's
	// identical-call cache. Protection against runaway loops stays with the
	// iteration/quota budgets.
	SkipLedgerDedup() bool

	// SafeInputSummary renders a sanitized one-line description of the tool
	// input for the ledger summary. Must not include secrets, full URLs with
	// query strings, headers, or body content.
	SafeInputSummary(input json.RawMessage) string

	// SuppressResultPreview disables logging/handling previews of the skill's
	// output (the result may embed credentials or large external content).
	SuppressResultPreview() bool
}

// SensitiveOutput is an optional marker interface for skills that produce
// results which must not be copied into approval prompts or other generated
// strings that leave the execution context.
type SensitiveOutput interface {
	IsSensitiveOutput() bool
}

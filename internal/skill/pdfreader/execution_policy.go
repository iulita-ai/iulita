package pdfreader

import "encoding/json"

// SkipLedgerDedup retains normal per-turn duplicate-call handling.
func (*Skill) SkipLedgerDedup() bool { return false }

// SafeInputSummary excludes document metadata from ledger summaries.
func (*Skill) SafeInputSummary(json.RawMessage) string { return "attached document" }

// SuppressResultPreview keeps document text and summaries out of INFO logs.
func (*Skill) SuppressResultPreview() bool { return true }

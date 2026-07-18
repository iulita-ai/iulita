package importer

import (
	"strings"
	"unicode"

	"github.com/google/uuid"
)

// importNamespace is a fixed UUIDv5 namespace for deriving deterministic dedup keys
// for imported memory facts (the facts table has no source_uuid column, so the key
// lives in the imported_fact_keys sidecar). It must never change or re-imports would
// duplicate every fact.
var importNamespace = uuid.MustParse("6f1d4c2a-0b3e-5a7c-9e21-3d8f4b6a1c05")

// factKey derives a stable UUIDv5 dedup key from the given parts. The same input
// always yields the same key, so re-importing an unchanged memory is a no-op.
func factKey(parts ...string) string {
	return uuid.NewSHA1(importNamespace, []byte(strings.Join(parts, "|"))).String()
}

// slug normalizes a heading into a stable key component: lowercased, runs of
// non-letter/digit characters collapsed to single hyphens, trimmed. Unicode letters
// and digits (CJK/Cyrillic/Hebrew, etc.) are preserved so non-Latin headings still
// produce content-derived — not position-derived — key components.
func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevHyphen := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			prevHyphen = false
			continue
		}
		if !prevHyphen && b.Len() > 0 {
			b.WriteByte('-')
			prevHyphen = true
		}
	}
	return strings.Trim(b.String(), "-")
}

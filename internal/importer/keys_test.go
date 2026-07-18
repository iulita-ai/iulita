package importer

import "testing"

func TestFactKeyDeterministic(t *testing.T) {
	a := factKey("acc", "conversations_memory", "work-context")
	b := factKey("acc", "conversations_memory", "work-context")
	if a != b {
		t.Fatalf("same input produced different keys: %q vs %q", a, b)
	}
	if factKey("acc", "conversations_memory", "personal-context") == a {
		t.Fatal("different heading must produce a different key")
	}
	if factKey("other", "conversations_memory", "work-context") == a {
		t.Fatal("different account must produce a different key")
	}
	// UUIDv5 string form.
	if len(a) != 36 {
		t.Errorf("expected 36-char uuid, got %q", a)
	}
}

func TestSlug(t *testing.T) {
	tests := map[string]string{
		"Work context":        "work-context",
		"Purpose & context":   "purpose-context",
		"  Current State!!  ": "current-state",
		"Watches & hardware:": "watches-hardware",
		"":                    "",
		"---":                 "",
		"Mixed_CASE 123":      "mixed-case-123",
	}
	for in, want := range tests {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSlugPreservesNonLatin(t *testing.T) {
	// Non-Latin headings must produce content-derived (non-empty) slugs so dedup keys
	// stay stable across re-imports instead of collapsing to positional fallbacks.
	for _, in := range []string{"中文标题", "Раздел", "עברית"} {
		if got := slug(in); got == "" {
			t.Errorf("slug(%q) collapsed to empty", in)
		}
	}
	// Distinct non-Latin headings must slug distinctly.
	if slug("中文标题") == slug("Раздел") {
		t.Error("distinct non-Latin headings produced the same slug")
	}
}

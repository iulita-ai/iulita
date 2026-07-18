package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMapMemoriesRealDump is an opt-in smoke test against a real Claude export. It is
// skipped unless IULITA_CLAUDE_DUMP_DIR points at an extracted dump, so no personal
// data lives in the repository. With the reference dump it pins the deterministic
// fact count (H3): 4 conversations_memory sections + 72 project-memory sections = 76.
func TestMapMemoriesRealDump(t *testing.T) {
	dir := os.Getenv("IULITA_CLAUDE_DUMP_DIR")
	if dir == "" {
		t.Skip("set IULITA_CLAUDE_DUMP_DIR to run against a real dump")
	}
	data, err := os.ReadFile(filepath.Join(dir, "memories.json"))
	if err != nil {
		t.Skipf("cannot read memories.json: %v", err)
	}
	facts, err := MapMemories(data, "admin")
	if err != nil {
		t.Fatalf("MapMemories(real): %v", err)
	}
	if got := len(facts); got != 76 {
		t.Errorf("real-dump fact count = %d, want 76 (4 conv + 72 project sections)", got)
	}
	seen := map[string]bool{}
	for _, f := range facts {
		if seen[f.DedupKey] {
			t.Errorf("duplicate dedup key in real dump: %q", f.DedupKey)
		}
		seen[f.DedupKey] = true
	}
}

func TestMapMemoriesConversationsMemorySections(t *testing.T) {
	data := []byte(`[{
		"account_uuid":"acc-1",
		"conversations_memory":"**Work context**\n\nStas leads teams.\n\n**Personal context**\n\nLives in a city. **Watches & hardware:** owns a BMW and a Sea-Gull watch.",
		"project_memories":{}
	}]`)
	facts, err := MapMemories(data, "admin")
	if err != nil {
		t.Fatalf("MapMemories: %v", err)
	}
	// Two real headings; the inline "**Watches & hardware:**" label must NOT start a
	// third section (it shares a line with body text).
	if len(facts) != 2 {
		t.Fatalf("expected 2 sections, got %d: %+v", len(facts), factContents(facts))
	}
	if !strings.HasPrefix(facts[0].Fact.Content, "[Claude memory] Work context") {
		t.Errorf("fact[0] content = %q", facts[0].Fact.Content)
	}
	if !strings.Contains(facts[1].Fact.Content, "Watches & hardware") {
		t.Errorf("inline label body lost: %q", facts[1].Fact.Content)
	}
	for _, f := range facts {
		if f.Fact.ChatID != ImportChatID || f.Fact.SourceType != ImportSourceType || f.Fact.UserID != "admin" {
			t.Errorf("fact metadata wrong: %+v", f.Fact)
		}
		if f.DedupKey == "" {
			t.Error("expected non-empty dedup key")
		}
	}
}

func TestMapMemoriesHeadinglessIsSingleFact(t *testing.T) {
	data := []byte(`[{"account_uuid":"acc-1","conversations_memory":"just some prose with no headings at all","project_memories":{}}]`)
	facts, err := MapMemories(data, "admin")
	if err != nil {
		t.Fatalf("MapMemories: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact for headingless memory, got %d", len(facts))
	}
	if !strings.Contains(facts[0].Fact.Content, "just some prose") {
		t.Errorf("body lost: %q", facts[0].Fact.Content)
	}
}

func TestMapMemoriesProjectSplitAndSingle(t *testing.T) {
	data := []byte(`[{
		"account_uuid":"acc-1",
		"conversations_memory":"",
		"project_memories":{
			"proj-multi":"**Purpose & context**\n\nGoal A.\n\n**Current state**\n\nState B.",
			"proj-single":"A single blob with no headings."
		}
	}]`)
	facts, err := MapMemories(data, "admin")
	if err != nil {
		t.Fatalf("MapMemories: %v", err)
	}
	// proj-multi → 2 section facts; proj-single → 1 whole fact.
	if len(facts) != 3 {
		t.Fatalf("expected 3 facts, got %d: %+v", len(facts), factContents(facts))
	}
	// Deterministic order: sorted project UUID (proj-multi before proj-single).
	if !strings.Contains(facts[0].Fact.Content, "proj-multi") || !strings.Contains(facts[0].Fact.Content, "Purpose & context") {
		t.Errorf("fact[0] unexpected: %q", facts[0].Fact.Content)
	}
	if !strings.Contains(facts[2].Fact.Content, "proj-single") {
		t.Errorf("fact[2] unexpected: %q", facts[2].Fact.Content)
	}
	// The single-blob project uses its natural UUID as the dedup key.
	if facts[2].DedupKey != "proj-single" {
		t.Errorf("expected natural key 'proj-single', got %q", facts[2].DedupKey)
	}
}

func TestMapMemoriesDeterministic(t *testing.T) {
	data := []byte(`[{
		"account_uuid":"acc-1",
		"conversations_memory":"**A**\n\nx\n\n**B**\n\ny",
		"project_memories":{"p2":"**H1**\n\na\n\n**H2**\n\nb","p1":"just prose"}
	}]`)
	// Expected: 2 conv sections + (p1: 1 whole) + (p2: 2 sections) = 5.
	first, err := MapMemories(data, "admin")
	if err != nil {
		t.Fatalf("MapMemories: %v", err)
	}
	if len(first) != 5 {
		t.Fatalf("expected deterministic 5 facts, got %d", len(first))
	}
	second, _ := MapMemories(data, "admin")
	if len(second) != len(first) {
		t.Fatalf("non-deterministic count: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].DedupKey != second[i].DedupKey || first[i].Fact.Content != second[i].Fact.Content {
			t.Fatalf("non-deterministic output at %d: %q vs %q", i, first[i].DedupKey, second[i].DedupKey)
		}
	}
}

func TestMapMemoriesDedupKeyCollisionDisambiguated(t *testing.T) {
	// Two distinct headings that slug to the same value ("purpose-context") must not
	// collapse to one dedup key, or Phase-2 ON CONFLICT would silently drop a section.
	data := []byte(`[{
		"account_uuid":"acc-1",
		"conversations_memory":"**Purpose & context**\n\nfirst body.\n\n**Purpose, context**\n\nsecond body.",
		"project_memories":{}
	}]`)
	facts, err := MapMemories(data, "admin")
	if err != nil {
		t.Fatalf("MapMemories: %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("expected 2 facts, got %d", len(facts))
	}
	if facts[0].DedupKey == facts[1].DedupKey {
		t.Fatalf("colliding headings produced identical dedup keys: %q", facts[0].DedupKey)
	}
	// Deterministic across runs.
	again, _ := MapMemories(data, "admin")
	if again[1].DedupKey != facts[1].DedupKey {
		t.Errorf("collision disambiguation not deterministic: %q vs %q", again[1].DedupKey, facts[1].DedupKey)
	}
}

func factContents(facts []MappedFact) []string {
	out := make([]string, len(facts))
	for i := range facts {
		c := facts[i].Fact.Content
		if len(c) > 50 {
			c = c[:50]
		}
		out[i] = c
	}
	return out
}

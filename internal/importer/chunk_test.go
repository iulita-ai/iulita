package importer

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestChunkSmallReturnsSingle(t *testing.T) {
	got := Chunk("short text", 1500)
	if len(got) != 1 || got[0] != "short text" {
		t.Fatalf("expected single chunk, got %v", got)
	}
}

func TestChunkEmpty(t *testing.T) {
	if got := Chunk("   ", 1500); got != nil {
		t.Fatalf("expected nil for blank text, got %v", got)
	}
}

func TestChunkRespectsMaxAndReassembles(t *testing.T) {
	// 10 paragraphs of ~100 chars each; with maxChars=250 they must split into
	// multiple chunks, each within the limit.
	para := strings.Repeat("word ", 20) // ~100 chars
	var b strings.Builder
	for i := 0; i < 10; i++ {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(strings.TrimSpace(para))
	}
	original := b.String()
	chunks := Chunk(original, 250)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if len(c) > 250 {
			t.Errorf("chunk %d exceeds max: %d chars", i, len(c))
		}
		if strings.TrimSpace(c) == "" {
			t.Errorf("chunk %d is blank", i)
		}
	}
	// Reassembly: the word sequence must be preserved (no dropped/duplicated words).
	if got := strings.Fields(strings.Join(chunks, " ")); strings.Join(got, " ") != strings.Join(strings.Fields(original), " ") {
		t.Errorf("chunk reassembly lost content:\n got %d words\nwant %d words", len(got), len(strings.Fields(original)))
	}
}

func TestChunkMultiByteStaysValidUTF8(t *testing.T) {
	// CJK has no inter-word whitespace, so this forms one giant word that hits the
	// hard-cut path. 1200 bytes / 400 runes; maxChars=1000 must not split a rune.
	text := strings.Repeat("字", 400)
	chunks := Chunk(text, 1000)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	var reassembled strings.Builder
	for i, c := range chunks {
		if !utf8.ValidString(c) {
			t.Errorf("chunk %d is not valid UTF-8", i)
		}
		if len(c) > 1000 {
			t.Errorf("chunk %d exceeds max: %d bytes", i, len(c))
		}
		reassembled.WriteString(c)
	}
	if reassembled.String() != text {
		t.Error("multi-byte reassembly lost content")
	}
}

func TestChunkHardCutsGiantWord(t *testing.T) {
	giant := strings.Repeat("x", 3500)
	chunks := Chunk(giant, 1000)
	if len(chunks) != 4 {
		t.Fatalf("expected 4 hard-cut chunks (1000+1000+1000+500), got %d", len(chunks))
	}
	total := 0
	for _, c := range chunks {
		if len(c) > 1000 {
			t.Errorf("hard-cut chunk exceeds max: %d", len(c))
		}
		total += len(c)
	}
	if total != 3500 {
		t.Errorf("hard-cut lost content: total %d, want 3500", total)
	}
}

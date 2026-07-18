package importer

import (
	"strings"
	"unicode/utf8"
)

// DefaultChunkChars is the target chunk size for embedding. The all-MiniLM-L6-v2 model
// has a 256-token window; ~1500 bytes keeps most chunks within it while avoiding tiny
// fragments. Sizes are measured in bytes; multi-byte (e.g. CJK) text therefore chunks
// more conservatively, but chunks are always cut on rune boundaries (valid UTF-8).
const DefaultChunkChars = 1500

// Chunk splits text into chunks of at most maxChars, preferring paragraph then word
// boundaries, and hard-cutting only a single word longer than maxChars. Chunking is
// used for embedding only — stored content is never chunked. Returns a single element
// for text at or below the limit, and never returns empty chunks.
func Chunk(text string, maxChars int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if maxChars <= 0 {
		maxChars = DefaultChunkChars
	}
	if len(text) <= maxChars {
		return []string{text}
	}

	var chunks []string
	var cur strings.Builder
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			chunks = append(chunks, s)
		}
		cur.Reset()
	}

	for _, para := range strings.Split(text, "\n\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		// A paragraph that fits: append it, flushing first if it would overflow.
		if len(para) <= maxChars {
			if cur.Len() > 0 && cur.Len()+2+len(para) > maxChars {
				flush()
			}
			if cur.Len() > 0 {
				cur.WriteString("\n\n")
			}
			cur.WriteString(para)
			continue
		}
		// Oversized paragraph: flush the accumulator, then split by words.
		flush()
		for _, word := range strings.Fields(para) {
			if len(word) > maxChars {
				// Single word longer than a chunk: hard-cut into pieces on rune
				// boundaries so no chunk contains invalid UTF-8 (CJK/emoji safe).
				flush()
				for len(word) > maxChars {
					cut := runeSafeCut(word, maxChars)
					chunks = append(chunks, word[:cut])
					word = word[cut:]
				}
				if word != "" {
					cur.WriteString(word)
				}
				continue
			}
			if cur.Len() > 0 && cur.Len()+1+len(word) > maxChars {
				flush()
			}
			if cur.Len() > 0 {
				cur.WriteString(" ")
			}
			cur.WriteString(word)
		}
		flush()
	}
	flush()
	return chunks
}

// runeSafeCut returns the largest index <= maxBytes that falls on a UTF-8 rune
// boundary, so s[:cut] is always valid UTF-8. If a single rune is larger than
// maxBytes (pathological), it returns that rune's full size to guarantee progress.
func runeSafeCut(s string, maxBytes int) int {
	if maxBytes >= len(s) {
		return len(s)
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	if cut == 0 {
		_, size := utf8.DecodeRuneInString(s)
		return size
	}
	return cut
}

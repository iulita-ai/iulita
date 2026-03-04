package telegram

import "strings"

const maxMessageLen = 4000

// splitMessage splits text into chunks that fit within maxLen characters.
// It tries to split at paragraph, line, then word boundaries.
// It tracks code block state to avoid splitting inside ``` blocks.
func splitMessage(text string, maxLen int) []string {
	if len(text) <= maxLen {
		return []string{text}
	}

	var chunks []string
	remaining := text
	inCodeBlock := false

	for len(remaining) > 0 {
		if len(remaining) <= maxLen {
			chunks = append(chunks, remaining)
			break
		}

		chunk := remaining[:maxLen]

		// Find the best split point.
		splitAt := -1

		// Try paragraph boundary (double newline).
		if idx := strings.LastIndex(chunk, "\n\n"); idx > 0 {
			splitAt = idx + 1
		}

		// Try line boundary.
		if splitAt == -1 {
			if idx := strings.LastIndex(chunk, "\n"); idx > 0 {
				splitAt = idx + 1
			}
		}

		// Try word boundary.
		if splitAt == -1 {
			if idx := strings.LastIndex(chunk, " "); idx > 0 {
				splitAt = idx + 1
			}
		}

		// Hard split as last resort.
		if splitAt == -1 {
			splitAt = maxLen
		}

		part := remaining[:splitAt]

		// Track code block state — count ``` occurrences in this chunk.
		count := strings.Count(part, "```")
		if count%2 == 1 {
			inCodeBlock = !inCodeBlock
		}

		// If we're splitting inside a code block, close it and reopen in next chunk.
		if inCodeBlock {
			part += "\n```"
			remaining = "```\n" + remaining[splitAt:]
			inCodeBlock = false
		} else {
			remaining = remaining[splitAt:]
		}

		chunks = append(chunks, strings.TrimRight(part, "\n"))
	}

	return chunks
}

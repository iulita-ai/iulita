// Package slack implements channel.InputChannel for Slack bots using Socket Mode.
package slack

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Slack message length limit per block text.
const maxMessageLen = 3000

var (
	// Markdown → mrkdwn conversions.
	reBold       = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reStrike     = regexp.MustCompile(`~~(.+?)~~`)
	reLink       = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	reHeading    = regexp.MustCompile(`(?m)^#{1,6}\s+(.+)$`)
	reCodeLang   = regexp.MustCompile("(?m)^```\\w*\n")
	reInlineCode = regexp.MustCompile("`([^`]+)`")
)

// ToMrkdwn converts standard Markdown to Slack's mrkdwn format.
func ToMrkdwn(md string) string {
	// Preserve code blocks from transformation.
	type codeBlock struct {
		placeholder string
		content     string
	}

	var blocks []codeBlock
	idx := 0
	result := md

	// Extract fenced code blocks first.
	for {
		start := strings.Index(result, "```")
		if start == -1 {
			break
		}
		end := strings.Index(result[start+3:], "```")
		if end == -1 {
			break
		}
		end += start + 3 + 3

		placeholder := "\x00CODEBLOCK" + strconv.Itoa(idx) + "\x00"
		content := result[start:end]
		// Strip language hint from opening fence.
		content = reCodeLang.ReplaceAllString(content, "```\n")

		blocks = append(blocks, codeBlock{placeholder: placeholder, content: content})
		result = result[:start] + placeholder + result[end:]
		idx++
	}

	// Extract inline code.
	var inlineBlocks []codeBlock
	inlineIdx := 0
	result = reInlineCode.ReplaceAllStringFunc(result, func(m string) string {
		placeholder := "\x00INLINE" + strconv.Itoa(inlineIdx) + "\x00"
		inlineBlocks = append(inlineBlocks, codeBlock{placeholder: placeholder, content: m})
		inlineIdx++
		return placeholder
	})

	// Apply conversions.
	result = reLink.ReplaceAllString(result, "<$2|$1>") // [text](url) → <url|text>
	result = reBold.ReplaceAllString(result, "*$1*")    // **bold** → *bold*
	result = reStrike.ReplaceAllString(result, "~$1~")  // ~~strike~~ → ~strike~
	result = reHeading.ReplaceAllString(result, "*$1*") // # Heading → *Heading*

	// Restore inline code.
	for _, b := range inlineBlocks {
		result = strings.Replace(result, b.placeholder, b.content, 1)
	}

	// Restore code blocks.
	for _, b := range blocks {
		result = strings.Replace(result, b.placeholder, b.content, 1)
	}

	return result
}

// splitMessage splits text into chunks of at most maxLen bytes, preferring
// newline boundaries and never splitting a UTF-8 rune. Returns nil for empty text.
func splitMessage(text string, maxLen int) []string {
	if text == "" {
		return nil
	}
	if len(text) <= maxLen {
		return []string{text}
	}

	var chunks []string
	for text != "" {
		if len(text) <= maxLen {
			chunks = append(chunks, text)
			break
		}

		idx := strings.LastIndex(text[:maxLen], "\n")
		if idx <= 0 {
			idx = runeBoundaryBefore(text, maxLen)
		}
		if idx <= 0 {
			// Pathological input (invalid UTF-8 with no rune boundary in window);
			// force-advance one byte so we always make progress.
			idx = 1
		}

		chunks = append(chunks, text[:idx])
		text = text[idx:]
		if text != "" && text[0] == '\n' {
			text = text[1:]
		}
	}
	return chunks
}

// truncateRunes returns s truncated to at most maxBytes, never cutting a UTF-8
// rune in half.
func truncateRunes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	return s[:runeBoundaryBefore(s, maxBytes)]
}

// runeBoundaryBefore returns the largest index i<=maxBytes such that s[:i]
// ends on a valid UTF-8 rune boundary.
func runeBoundaryBefore(s string, maxBytes int) int {
	if maxBytes >= len(s) {
		return len(s)
	}
	i := maxBytes
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}

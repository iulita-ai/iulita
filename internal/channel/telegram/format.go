package telegram

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	// Markdown → Telegram legacy Markdown conversions.
	reBold       = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reBoldUnder  = regexp.MustCompile(`__(.+?)__`)
	reStrike     = regexp.MustCompile(`~~(.+?)~~`)
	reHeading    = regexp.MustCompile(`(?m)^#{1,6}[ \t]+(.+)$`)
	reBullet     = regexp.MustCompile(`(?m)^[-*][ \t]+`)
	reCodeLang   = regexp.MustCompile("(?m)^```\\w*\n")
	reInlineCode = regexp.MustCompile("`([^`]+)`")
)

// toTelegramMarkdown converts standard Markdown (LLM output) to Telegram's
// legacy Markdown dialect: **bold** → *bold*, # Heading → *Heading*,
// strikethrough markers are stripped (unsupported), - bullets become • .
// Code blocks and inline code are preserved as-is.
//
// Without conversion, Telegram rejects **bold** pairs ("can't parse entities")
// and the plain-text fallback sends the raw markers to the user.
func toTelegramMarkdown(md string) string {
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
		// Strip language hint from opening fence (legacy Markdown has no syntax highlight).
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

	// Apply conversions. Headings first (with inner bold normalized) so that
	// "## **Important** notice" doesn't produce broken nested asterisks.
	result = reHeading.ReplaceAllStringFunc(result, headingToTelegramBold)
	result = reBold.ReplaceAllString(result, "*$1*")      // **bold** → *bold*
	result = reBoldUnder.ReplaceAllString(result, "*$1*") // __bold__ → *bold*
	result = reStrike.ReplaceAllString(result, "$1")      // ~~strike~~ → strike (unsupported)
	result = reBullet.ReplaceAllString(result, "• ")      // - item / * item → • item

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

// headingToTelegramBold converts one "# Heading" line to Telegram bold.
// Inner **bold** markers collapse to *bold* first; if the line then already
// starts or ends with a bold marker, it is returned as-is to avoid broken
// nested asterisks (legacy Markdown cannot nest entities).
func headingToTelegramBold(heading string) string {
	m := reHeading.FindStringSubmatch(heading)
	if m == nil {
		return heading
	}
	text := reBold.ReplaceAllString(m[1], "*$1*")
	text = reBoldUnder.ReplaceAllString(text, "*$1*")
	if strings.HasPrefix(text, "*") || strings.HasSuffix(text, "*") {
		return text
	}
	return "*" + text + "*"
}

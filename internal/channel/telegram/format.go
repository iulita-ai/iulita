package telegram

import (
	"fmt"
	"strings"

	"github.com/rivo/uniseg"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extAst "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// maxMessageLen is the Telegram message length limit with headroom for entity markup.
const maxMessageLen = 4000

var mdParser = goldmark.New(
	goldmark.WithExtensions(extension.Table, extension.Strikethrough, extension.Linkify),
).Parser()

// htmlEscaper covers the characters Telegram's HTML mode treats specially in
// both text content and attribute values.
var htmlEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	`"`, "&quot;",
)

func escapeHTML(s string) string {
	return htmlEscaper.Replace(s)
}

// skipChildrenKinds are the nodes whose content is fully emitted on enter:
// their children must not be walked again.
var skipChildrenKinds = map[ast.NodeKind]struct{}{
	ast.KindCodeBlock:       {},
	ast.KindFencedCodeBlock: {},
	ast.KindCodeSpan:        {},
	ast.KindRawHTML:         {},
	ast.KindHTMLBlock:       {},
	ast.KindAutoLink:        {},
}

// toTelegramHTML converts standard Markdown (LLM output) to Telegram's HTML
// dialect using goldmark's AST. Supported entities map to the tags Telegram
// allows (<b>, <i>, <s>, <code>, <pre>, <a>, <blockquote>); headings become
// bold lines, list items become "• "/"1. " prefixed lines and tables collapse
// into a monospace block. Raw HTML in the input is stripped.
func toTelegramHTML(md string) string {
	return strings.Join(toTelegramHTMLBlocks(md), "\n\n")
}

// telegramHTMLChunks converts Markdown to HTML message chunks of at most
// maxMessageLen bytes. Splitting happens on top-level block boundaries so
// HTML tags are never cut in half; an oversized block is hard-split at
// line/word boundaries, re-wrapping <pre> content.
func telegramHTMLChunks(md string) []string {
	const maxLen = maxMessageLen
	blocks := toTelegramHTMLBlocks(md)
	if len(blocks) == 0 {
		return nil
	}

	var chunks []string
	cur := blocks[0]
	for _, b := range blocks[1:] {
		if len(cur)+2+len(b) <= maxLen {
			cur += "\n\n" + b
		} else {
			chunks = append(chunks, cur)
			cur = b
		}
	}
	chunks = append(chunks, cur)

	var out []string
	for _, ch := range chunks {
		if len(ch) <= maxLen {
			out = append(out, ch)
			continue
		}
		out = append(out, hardSplitHTML(ch, maxLen)...)
	}
	return out
}

// hardSplitHTML splits a single oversized HTML block at line, then word
// boundaries. A <pre> block is unwrapped first so each piece is a valid
// fenced block again.
func hardSplitHTML(block string, maxLen int) []string {
	trimmed := strings.TrimSuffix(strings.TrimPrefix(block, "<pre>"), "</pre>")
	wasPre := trimmed != block

	var out []string
	var cur string
	flush := func() {
		if cur != "" {
			if wasPre {
				cur = "<pre>" + cur + "\n</pre>"
			}
			out = append(out, cur)
		}
		cur = ""
	}
	for _, part := range strings.Split(trimmed, "\n") {
		candidate := part
		if cur != "" {
			candidate = cur + "\n" + part
		}
		if len(candidate) > maxLen && cur != "" {
			flush()
			candidate = part
		}
		// Single line still too long: split on word boundaries.
		for len(candidate) > maxLen {
			cut := strings.LastIndex(candidate[:maxLen], " ")
			if cut <= 0 {
				cut = maxLen
			}
			piece := candidate[:cut]
			if wasPre {
				piece = "<pre>" + piece + "\n</pre>"
			}
			out = append(out, piece)
			candidate = strings.TrimPrefix(candidate[cut:], " ")
		}
		cur = candidate
	}
	flush()
	return out
}

// toTelegramHTMLBlocks renders each top-level Markdown block as a standalone
// HTML fragment.
func toTelegramHTMLBlocks(md string) []string {
	source := []byte(md)
	doc := mdParser.Parse(text.NewReader(source))

	e := &htmlEmitter{source: source, blocks: make([]string, 0, 8)}
	//nolint:errcheck,gosec // the emitter's walk never returns an error
	ast.Walk(doc, e.walk)
	e.finishBlock()

	var out []string
	for _, b := range e.blocks {
		if strings.TrimSpace(b) != "" {
			out = append(out, b)
		}
	}
	return out
}

type listContext struct {
	ordered bool
	start   int
	counter int
	prefix  string // indentation for nested lists ("", "  ", ...)
}

// writeString appends s to the builder; strings.Builder never returns an error.
func writeString(b *strings.Builder, s string) {
	b.WriteString(s)
}

// nodeAs asserts n to T. The kind switch at the call site guarantees the match.
func nodeAs[T ast.Node](n ast.Node) T {
	if v, ok := n.(T); ok {
		return v
	}
	var zero T
	return zero
}

// htmlEmitter walks a goldmark AST and writes Telegram-flavored HTML,
// one fragment per top-level block.
type htmlEmitter struct {
	source    []byte
	blocks    []string
	cur       strings.Builder // current block being written
	sep       string          // separator to emit before the next block
	lists     []listContext
	plain     int             // >0 while tags are suppressed (table cells render inside <pre>)
	cellText  strings.Builder // text of the table cell being walked
	rowCells  []string        // cells of the table row being walked
	tableRows [][]string      // completed rows of the table being walked
}

// block starts a new block, emitting the pending inline separator. Top-level
// blocks get their spacing from the join in toTelegramHTML instead.
func (e *htmlEmitter) block(n ast.Node) {
	if n.Parent() != nil && n.Parent().Kind() == ast.KindDocument {
		e.sep = ""
		return
	}
	writeString(&e.cur, e.sep)
	e.sep = ""
}

// finishBlock closes the current top-level block fragment.
func (e *htmlEmitter) finishBlock() {
	e.blocks = append(e.blocks, e.cur.String())
	e.cur.Reset()
	e.sep = ""
}

// write emits text into the current target (table cells buffer their text
// until the cell closes).
func (e *htmlEmitter) write(s string) {
	if e.plain > 0 {
		writeString(&e.cellText, s)
		return
	}
	writeString(&e.cur, s)
}

// tag writes an HTML tag unless suppressed (inside table cells).
func (e *htmlEmitter) tag(s string) {
	if e.plain == 0 {
		writeString(&e.cur, s)
	}
}

func (e *htmlEmitter) walk(n ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		e.enter(n)
		if _, ok := skipChildrenKinds[n.Kind()]; ok {
			return ast.WalkSkipChildren, nil
		}
	} else {
		e.exit(n)
	}
	return ast.WalkContinue, nil
}

func (e *htmlEmitter) enter(n ast.Node) {
	switch n.Kind() {
	case ast.KindParagraph:
		if len(e.lists) == 0 {
			e.block(n)
		}
	case ast.KindHeading:
		e.block(n)
		e.tag("<b>")
	case ast.KindThematicBreak:
		e.block(n)
		e.write("───────")
		e.sep = "\n\n"
	case ast.KindCodeBlock, ast.KindFencedCodeBlock:
		e.block(n)
		e.tag("<pre>")
		lines := n.Lines()
		for i := 0; i < lines.Len(); i++ {
			seg := lines.At(i)
			e.write(escapeHTML(string(seg.Value(e.source))))
		}
		e.tag("</pre>")
		e.sep = "\n\n"
	case ast.KindBlockquote:
		e.block(n)
		e.tag("<blockquote>")
		e.sep = ""
	case ast.KindList:
		if len(e.lists) > 0 {
			e.write("\n")
		}
		e.sep = ""
		l := nodeAs[*ast.List](n)
		prefix := ""
		if len(e.lists) > 0 {
			prefix = e.lists[len(e.lists)-1].prefix + "  "
		}
		e.lists = append(e.lists, listContext{
			ordered: l.IsOrdered(),
			start:   l.Start,
			prefix:  prefix,
		})
	case ast.KindListItem:
		e.block(n)
		lc := &e.lists[len(e.lists)-1]
		lc.counter++
		marker := "• "
		if lc.ordered {
			marker = fmt.Sprintf("%d. ", lc.start+lc.counter-1)
		}
		e.write(lc.prefix + marker)
		e.sep = ""
	case ast.KindText:
		t := nodeAs[*ast.Text](n)
		e.write(escapeHTML(string(t.Segment.Value(e.source))))
		if t.SoftLineBreak() || t.HardLineBreak() {
			e.write("\n")
		}
	case ast.KindCodeSpan:
		var raw []byte
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			if t, ok := c.(*ast.Text); ok {
				raw = append(raw, t.Segment.Value(e.source)...)
			}
		}
		code := escapeHTML(string(raw))
		if e.plain == 0 {
			code = "<code>" + code + "</code>"
		}
		e.write(code)
	case ast.KindEmphasis:
		if nodeAs[*ast.Emphasis](n).Level == 2 {
			e.tag("<b>")
		} else {
			e.tag("<i>")
		}
	case extAst.KindStrikethrough:
		e.tag("<s>")
	case ast.KindLink:
		l := nodeAs[*ast.Link](n)
		e.tag(`<a href="` + escapeHTML(string(l.Destination)) + `">`)
	case ast.KindAutoLink:
		l := nodeAs[*ast.AutoLink](n)
		url := escapeHTML(string(l.URL(e.source)))
		e.tag(`<a href="` + url + `">`)
		e.write(escapeHTML(string(l.Label(e.source))))
	case ast.KindImage:
		img := nodeAs[*ast.Image](n)
		e.tag(`<a href="` + escapeHTML(string(img.Destination)) + `">🖼 `)
	case extAst.KindTable:
		e.block(n)
		e.tableRows = e.tableRows[:0]
		e.sep = ""
	case extAst.KindTableHeader, extAst.KindTableRow:
		e.rowCells = e.rowCells[:0]
	case extAst.KindTableCell:
		e.plain++
	}
}

func (e *htmlEmitter) exit(n ast.Node) {
	switch n.Kind() {
	case ast.KindParagraph:
		if len(e.lists) == 0 {
			e.sep = "\n\n"
		}
	case ast.KindHeading:
		e.tag("</b>")
		e.sep = "\n\n"
	case ast.KindBlockquote:
		e.tag("</blockquote>")
		e.sep = "\n\n"
	case ast.KindList:
		e.lists = e.lists[:len(e.lists)-1]
		e.sep = "\n\n"
	case ast.KindListItem:
		e.sep = "\n"
	case ast.KindEmphasis:
		if nodeAs[*ast.Emphasis](n).Level == 2 {
			e.tag("</b>")
		} else {
			e.tag("</i>")
		}
	case extAst.KindStrikethrough:
		e.tag("</s>")
	case ast.KindLink, ast.KindAutoLink, ast.KindImage:
		e.tag("</a>")
	case extAst.KindTableCell:
		e.plain--
		e.rowCells = append(e.rowCells, e.cellText.String())
		e.cellText.Reset()
	case extAst.KindTableHeader, extAst.KindTableRow:
		e.tableRows = append(e.tableRows, e.rowCells)
		e.rowCells = nil
	case extAst.KindTable:
		e.renderTable()
		e.sep = "\n\n"
	}

	// A block that is a direct child of the document becomes its own fragment.
	if n.Parent() != nil && n.Parent().Kind() == ast.KindDocument {
		e.finishBlock()
	}
}

// renderTable emits the buffered rows as a column-aligned monospace table:
// cells are padded to the widest cell of their column so the pipes line up,
// with a dashed separator under the header row.
//
// Column widths use display width (uniseg, grapheme-cluster based), not rune
// count: emoji render two cells wide and variation selectors zero in
// Telegram's monospace font, so rune-based padding would misalign any column
// containing an emoji.
func (e *htmlEmitter) renderTable() {
	rows := e.tableRows
	e.tableRows = nil
	if len(rows) == 0 {
		return
	}

	cols := 0
	for _, r := range rows {
		if len(r) > cols {
			cols = len(r)
		}
	}
	widths := make([]int, cols)
	for _, r := range rows {
		for i, c := range r {
			if w := uniseg.StringWidth(c); w > widths[i] {
				widths[i] = w
			}
		}
	}

	pad := func(cell string, width int) string {
		return cell + strings.Repeat(" ", max(width-uniseg.StringWidth(cell), 0))
	}

	e.tag("<pre>")
	for ri, r := range rows {
		if ri == 1 {
			// Dashed separator under the header row.
			dashes := make([]string, cols)
			for i := range dashes {
				dashes[i] = strings.Repeat("-", widths[i])
			}
			e.write(strings.Join(dashes, "-+-"))
			e.write("\n")
		}
		cells := make([]string, cols)
		for i := range cells {
			cell := ""
			if i < len(r) {
				cell = r[i]
			}
			cells[i] = pad(cell, widths[i])
		}
		e.write(strings.TrimRight(strings.Join(cells, " | "), " "))
		if ri != len(rows)-1 {
			e.write("\n")
		}
	}
	e.tag("</pre>")
}

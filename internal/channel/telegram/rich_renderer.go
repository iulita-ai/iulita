package telegram

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/yuin/goldmark/ast"
	ext "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

func richInline(n ast.Node, src []byte, depth int) models.RichText {
	if depth > 24 {
		return models.RichText{PlainText: "[content limit]"}
	}
	var parts []models.RichText
	for child := n.FirstChild(); child != nil; child = child.NextSibling() {
		var t models.RichText
		switch v := child.(type) {
		case *ast.Text:
			t.PlainText = string(v.Segment.Value(src))
			if v.SoftLineBreak() || v.HardLineBreak() {
				t.PlainText += "\n"
			}
		case *ast.String:
			t.PlainText = string(v.Value)
		case *ast.Emphasis:
			inner := richInline(v, src, depth+1)
			if v.Level == 2 {
				t = models.RichText{Type: models.RichTextTypeBold, RichTextBold: &models.RichTextBold{Text: inner}}
			} else {
				t = models.RichText{Type: models.RichTextTypeItalic, RichTextItalic: &models.RichTextItalic{Text: inner}}
			}
		case *ast.CodeSpan:
			t = models.RichText{Type: models.RichTextTypeCode, RichTextCode: &models.RichTextCode{Text: richInline(v, src, depth+1)}}
		case *ast.Link:
			inner := richInline(v, src, depth+1)
			u, e := url.Parse(string(v.Destination))
			if e == nil && (u.Scheme == "https" || u.Scheme == "http" || u.Scheme == "mailto") {
				t = models.RichText{Type: models.RichTextTypeURL, RichTextURL: &models.RichTextURL{Text: inner, URL: u.String()}}
			} else {
				t = inner
			}
		case *ast.AutoLink:
			label := string(v.Label(src))
			u, e := url.Parse(string(v.URL(src)))
			t.PlainText = label
			if e == nil && (u.Scheme == "https" || u.Scheme == "http") {
				t = models.RichText{Type: models.RichTextTypeURL, RichTextURL: &models.RichTextURL{Text: models.RichText{PlainText: label}, URL: u.String()}}
			}
		case *ext.Strikethrough:
			t = models.RichText{Type: models.RichTextTypeStrikethrough, RichTextStrikethrough: &models.RichTextStrikethrough{Text: richInline(v, src, depth+1)}}
		case *ast.Image:
			t.PlainText = "[image] " + richPlain(richInline(v, src, depth+1))
		case *ast.RawHTML:
			t.PlainText = "" // never reinterpret model HTML as active markup
		default:
			t = richInline(child, src, depth+1)
		}
		parts = append(parts, t)
	}
	return models.RichText{Array: parts}
}
func richBlocks(parent ast.Node, src []byte, depth int) []models.InputRichBlock {
	if depth > 20 {
		return nil
	}
	var blocks []models.InputRichBlock
	for n := parent.FirstChild(); n != nil; n = n.NextSibling() {
		if len(blocks) >= 128 {
			break
		}
		switch v := n.(type) {
		case *ast.Paragraph, *ast.TextBlock:
			blocks = append(blocks, models.InputRichBlock{Type: models.RichBlockTypeParagraph, InputRichBlockParagraph: &models.InputRichBlockParagraph{Text: richInline(n, src, 0)}})
		case *ast.Heading:
			blocks = append(blocks, models.InputRichBlock{Type: models.RichBlockTypeSectionHeading, InputRichBlockSectionHeading: &models.InputRichBlockSectionHeading{Text: richInline(n, src, 0), Size: v.Level}})
		case *ast.FencedCodeBlock:
			var b strings.Builder
			for j := 0; j < v.Lines().Len(); j++ {
				segment := v.Lines().At(j)
				b.Write(segment.Value(src))
			}
			blocks = append(blocks, models.InputRichBlock{Type: models.RichBlockTypePreformatted, InputRichBlockPreformatted: &models.InputRichBlockPreformatted{Text: models.RichText{PlainText: b.String()}, Language: string(v.Language(src))}})
		case *ast.CodeBlock:
			var b strings.Builder
			for j := 0; j < v.Lines().Len(); j++ {
				segment := v.Lines().At(j)
				b.Write(segment.Value(src))
			}
			blocks = append(blocks, models.InputRichBlock{Type: models.RichBlockTypePreformatted, InputRichBlockPreformatted: &models.InputRichBlockPreformatted{Text: models.RichText{PlainText: b.String()}}})
		case *ast.ThematicBreak:
			blocks = append(blocks, models.InputRichBlock{Type: models.RichBlockTypeDivider, InputRichBlockDivider: &models.InputRichBlockDivider{}})
		case *ast.Blockquote:
			blocks = append(blocks, models.InputRichBlock{Type: models.RichBlockTypeBlockQuotation, InputRichBlockBlockQuotation: &models.InputRichBlockBlockQuotation{Blocks: richBlocks(v, src, depth+1)}})
		case *ast.List:
			list := &models.InputRichBlockList{}
			i := v.Start
			for item := v.FirstChild(); item != nil; item = item.NextSibling() {
				listType := ""
				value := 0
				if v.IsOrdered() {
					listType = "1"
					value = i
				}
				list.Items = append(list.Items, models.InputRichBlockListItem{Type: listType, Value: value, Blocks: richBlocks(item, src, depth+1)})
				i++
			}
			blocks = append(blocks, models.InputRichBlock{Type: models.RichBlockTypeList, InputRichBlockList: list})
		case *ext.Table:
			table := &models.InputRichBlockTable{IsBordered: true, IsCompact: true}
			for row := v.FirstChild(); row != nil; row = row.NextSibling() {
				var cells []models.RichBlockTableCell
				_, header := row.(*ext.TableHeader)
				for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
					t := richInline(cell, src, 0)
					cells = append(cells, models.RichBlockTableCell{Text: &t, IsHeader: header, Colspan: 1, Rowspan: 1, Align: "left", Valign: "top"})
				}
				table.Cells = append(table.Cells, cells)
			}
			blocks = append(blocks, models.InputRichBlock{Type: models.RichBlockTypeTable, InputRichBlockTable: table})
		default:
			children := richBlocks(n, src, depth+1)
			if len(children) > 0 {
				blocks = append(blocks, children...)
			} else if t := strings.TrimSpace(richPlain(richInline(n, src, depth+1))); t != "" {
				blocks = append(blocks, models.InputRichBlock{Type: models.RichBlockTypeParagraph, InputRichBlockParagraph: &models.InputRichBlockParagraph{Text: models.RichText{PlainText: t}}})
			}
		}
	}
	return blocks
}
func renderRichMessage(markdown string) (models.InputRichMessage, error) {
	if len(markdown) > 12000 {
		return models.InputRichMessage{}, fmt.Errorf("rich reply too large")
	}
	src := []byte(markdown)
	doc := mdParser.Parse(text.NewReader(src))
	// Reject rather than silently truncate a deeply nested or long block tree;
	// the caller can send the complete original through the chunked fallback.
	children := map[ast.Node]int{}
	nodes, depth := 0, 0
	if err := ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			nodes++
			depth++
			children[n.Parent()]++
			if nodes > 4096 || depth > 20 || children[n.Parent()] > 128 {
				return ast.WalkStop, fmt.Errorf("rich tree exceeds budget")
			}
		} else {
			depth--
		}
		return ast.WalkContinue, nil
	}); err != nil {
		return models.InputRichMessage{}, err
	}
	blocks := richBlocks(doc, src, 0)
	out := models.InputRichMessage{Blocks: blocks}
	raw, err := json.Marshal(out)
	if err != nil || len(raw) > 64*1024 {
		return models.InputRichMessage{}, fmt.Errorf("rich reply exceeds budget")
	}
	if len(blocks) == 0 {
		return models.InputRichMessage{}, fmt.Errorf("empty rich reply")
	}
	return out, nil
}

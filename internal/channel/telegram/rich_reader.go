package telegram

import (
	"strings"
	"unicode/utf8"

	"github.com/go-telegram/bot/models"
)

const richTextLimit = 16384

// richReader consumes only SDK display fields. Transport IDs, wallet metadata,
// URLs and callback payloads are never reflected into model context.
type richReader struct {
	out   strings.Builder
	nodes int
}

func (r *richReader) add(s string) {
	left := richTextLimit - r.out.Len()
	if left <= 0 {
		return
	}
	if len(s) > left {
		s = s[:left]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	r.out.WriteString(s)
}
func (r *richReader) text(v models.RichText, depth int) {
	r.nodes++
	if depth > 24 || r.nodes > 4096 || r.out.Len() >= richTextLimit {
		return
	}
	if v.Array != nil {
		for i := range v.Array {
			r.text(v.Array[i], depth+1)
		}
		return
	}
	switch v.Type {
	case "":
		r.add(v.PlainText)
	case models.RichTextTypeBold:
		if v.RichTextBold != nil {
			r.text(v.RichTextBold.Text, depth+1)
		}
	case models.RichTextTypeItalic:
		if v.RichTextItalic != nil {
			r.text(v.RichTextItalic.Text, depth+1)
		}
	case models.RichTextTypeUnderline:
		if v.RichTextUnderline != nil {
			r.text(v.RichTextUnderline.Text, depth+1)
		}
	case models.RichTextTypeStrikethrough:
		if v.RichTextStrikethrough != nil {
			r.text(v.RichTextStrikethrough.Text, depth+1)
		}
	case models.RichTextTypeSpoiler:
		if v.RichTextSpoiler != nil {
			r.text(v.RichTextSpoiler.Text, depth+1)
		}
	case models.RichTextTypeDateTime:
		if v.RichTextDateTime != nil {
			r.text(v.RichTextDateTime.Text, depth+1)
		}
	case models.RichTextTypeTextMention:
		if v.RichTextTextMention != nil {
			r.text(v.RichTextTextMention.Text, depth+1)
		}
	case models.RichTextTypeSubscript:
		if v.RichTextSubscript != nil {
			r.text(v.RichTextSubscript.Text, depth+1)
		}
	case models.RichTextTypeSuperscript:
		if v.RichTextSuperscript != nil {
			r.text(v.RichTextSuperscript.Text, depth+1)
		}
	case models.RichTextTypeMarked:
		if v.RichTextMarked != nil {
			r.text(v.RichTextMarked.Text, depth+1)
		}
	case models.RichTextTypeCode:
		if v.RichTextCode != nil {
			r.text(v.RichTextCode.Text, depth+1)
		}
	case models.RichTextTypeCustomEmoji:
		if v.RichTextCustomEmoji != nil {
			r.add(v.RichTextCustomEmoji.AlternativeText)
		}
	case models.RichTextTypeMathematicalExpression:
		if v.RichTextMathematicalExpression != nil {
			r.add(v.RichTextMathematicalExpression.Expression)
		}
	case models.RichTextTypeURL:
		if v.RichTextURL != nil {
			r.text(v.RichTextURL.Text, depth+1)
		}
	case models.RichTextTypeEmailAddress:
		if v.RichTextEmailAddress != nil {
			r.text(v.RichTextEmailAddress.Text, depth+1)
		}
	case models.RichTextTypePhoneNumber:
		if v.RichTextPhoneNumber != nil {
			r.text(v.RichTextPhoneNumber.Text, depth+1)
		}
	case models.RichTextTypeBankCardNumber:
		r.add("[redacted]")
	case models.RichTextTypeTonAddress:
		r.add("[redacted]")
	case models.RichTextTypeMention:
		if v.RichTextMention != nil {
			r.text(v.RichTextMention.Text, depth+1)
		}
	case models.RichTextTypeHashtag:
		if v.RichTextHashtag != nil {
			r.text(v.RichTextHashtag.Text, depth+1)
		}
	case models.RichTextTypeCashtag:
		if v.RichTextCashtag != nil {
			r.text(v.RichTextCashtag.Text, depth+1)
		}
	case models.RichTextTypeBotCommand:
		if v.RichTextBotCommand != nil {
			r.text(v.RichTextBotCommand.Text, depth+1)
		}
	case models.RichTextTypeButton:
		if v.RichTextButton != nil {
			r.text(v.RichTextButton.Button.Text, depth+1)
		}
	case models.RichTextTypeAnchor: // Anchors have no displayed text.
	case models.RichTextTypeAnchorLink:
		if v.RichTextAnchorLink != nil {
			r.text(v.RichTextAnchorLink.Text, depth+1)
		}
	case models.RichTextTypeReference:
		if v.RichTextReference != nil {
			r.text(v.RichTextReference.Text, depth+1)
		}
	case models.RichTextTypeReferenceLink:
		if v.RichTextReferenceLink != nil {
			r.text(v.RichTextReferenceLink.Text, depth+1)
		}
	default:
		r.add("[unsupported rich text]")
	}
}
func (r *richReader) caption(c *models.RichBlockCaption, d int) {
	if c == nil {
		return
	}
	r.text(c.Text, d+1)
	if c.Credit != nil {
		r.add(" — ")
		r.text(*c.Credit, d+1)
	}
}
func (r *richReader) blocks(blocks []models.RichBlock, d int) {
	for i := range blocks {
		r.block(blocks[i], d+1)
		r.add("\n")
	}
}
func (r *richReader) block(b models.RichBlock, d int) {
	r.nodes++
	if d > 24 || r.nodes > 4096 || r.out.Len() >= richTextLimit {
		return
	}
	switch b.Type {
	case models.RichBlockTypeParagraph:
		if v := b.RichBlockParagraph; v != nil {
			r.text(v.Text, d+1)
		}
	case models.RichBlockTypeSectionHeading:
		if v := b.RichBlockSectionHeading; v != nil {
			r.text(v.Text, d+1)
		}
	case models.RichBlockTypePreformatted:
		if v := b.RichBlockPreformatted; v != nil {
			r.text(v.Text, d+1)
		}
	case models.RichBlockTypeFooter:
		if v := b.RichBlockFooter; v != nil {
			r.text(v.Text, d+1)
		}
	case models.RichBlockTypeThinking:
		if v := b.RichBlockThinking; v != nil {
			r.text(v.Text, d+1)
		}
	case models.RichBlockTypeExpandableBlockQuotation:
		if v := b.RichBlockExpandableBlockQuotation; v != nil {
			r.text(v.Text, d+1)
			if v.Credit != nil {
				r.add(" — ")
				r.text(*v.Credit, d+1)
			}
		}
	case models.RichBlockTypePullQuotation:
		if v := b.RichBlockPullQuotation; v != nil {
			r.text(v.Text, d+1)
			if v.Credit != nil {
				r.add(" — ")
				r.text(*v.Credit, d+1)
			}
		}
	case models.RichBlockTypeDivider:
		r.add("---")
	case models.RichBlockTypeAnchor:
	case models.RichBlockTypeMathematicalExpression:
		if v := b.RichBlockMathematicalExpression; v != nil {
			r.add(v.Expression)
		}
	case models.RichBlockTypeList:
		if v := b.RichBlockList; v != nil {
			for _, item := range v.Items {
				if item.HasCheckbox {
					if item.IsChecked {
						r.add("[x] ")
					} else {
						r.add("[ ] ")
					}
				} else {
					r.add(item.Label + " ")
				}
				r.blocks(item.Blocks, d+1)
			}
		}
	case models.RichBlockTypeBlockQuotation:
		if v := b.RichBlockBlockQuotation; v != nil {
			r.blocks(v.Blocks, d+1)
			if v.Credit != nil {
				r.text(*v.Credit, d+1)
			}
		}
	case models.RichBlockTypeDetails:
		if v := b.RichBlockDetails; v != nil {
			r.text(v.Summary, d+1)
			r.add("\n")
			r.blocks(v.Blocks, d+1)
		}
	case models.RichBlockTypeTable:
		if v := b.RichBlockTable; v != nil {
			for _, row := range v.Cells {
				for j, cell := range row {
					if j > 0 {
						r.add(" | ")
					}
					if cell.Text != nil {
						r.text(*cell.Text, d+1)
					}
				}
				r.add("\n")
			}
			if v.Caption != nil {
				r.text(*v.Caption, d+1)
			}
		}
	case models.RichBlockTypeButtons:
		if v := b.RichBlockButtons; v != nil {
			for i := range v.Buttons {
				r.text(v.Buttons[i].Text, d+1)
				r.add(" ")
			}
		}
	case models.RichBlockTypeCollage:
		if v := b.RichBlockCollage; v != nil {
			r.blocks(v.Blocks, d+1)
			r.caption(v.Caption, d+1)
		}
	case models.RichBlockTypeSlideshow:
		if v := b.RichBlockSlideshow; v != nil {
			r.blocks(v.Blocks, d+1)
			r.caption(v.Caption, d+1)
		}
	case models.RichBlockTypeMap:
		if v := b.RichBlockMap; v != nil {
			r.add("[map] ")
			r.caption(v.Caption, d+1)
		}
	case models.RichBlockTypeAnimation:
		if v := b.RichBlockAnimation; v != nil {
			r.add("[animation] ")
			r.caption(v.Caption, d+1)
		}
	case models.RichBlockTypeAudio:
		if v := b.RichBlockAudio; v != nil {
			r.add("[audio] ")
			r.caption(v.Caption, d+1)
		}
	case models.RichBlockTypeDocument:
		if v := b.RichBlockDocument; v != nil {
			r.add("[document] ")
			r.caption(v.Caption, d+1)
		}
	case models.RichBlockTypePhoto:
		if v := b.RichBlockPhoto; v != nil {
			r.add("[photo] ")
			r.caption(v.Caption, d+1)
		}
	case models.RichBlockTypeVideo:
		if v := b.RichBlockVideo; v != nil {
			r.add("[video] ")
			r.caption(v.Caption, d+1)
		}
	case models.RichBlockTypeVoiceNote:
		if v := b.RichBlockVoiceNote; v != nil {
			r.add("[voicenote] ")
			r.caption(v.Caption, d+1)
		}
	default:
		r.add("[unsupported rich block]")
	}
}
func richPlain(v any) string {
	r := new(richReader)
	switch x := v.(type) {
	case *models.RichMessage:
		if x != nil {
			r.blocks(x.Blocks, 0)
		}
	case models.RichMessage:
		r.blocks(x.Blocks, 0)
	case models.RichText:
		r.text(x, 0)
	case []models.RichText:
		for i := range x {
			r.text(x[i], 0)
		}
	case string:
		r.add(x)
	default:
		r.add("[unsupported rich content]")
	}
	return strings.TrimSpace(r.out.String())
}

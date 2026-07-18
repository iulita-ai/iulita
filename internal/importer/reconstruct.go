package importer

import "strings"

// reconstructMessage builds the human-readable content of a message and reports how
// many binary files were skipped.
//
// Rules (locked):
//   - Prefer message.text; fall back to concatenating ONLY type=="text" content
//     blocks (thinking/tool_use/tool_result/token_budget are never included — a
//     security invariant, tool_result may carry secrets/API responses).
//   - Inline each attachment's extracted_content (text uploads).
//   - Binary files (files[]) have no bytes in the export: count them and, only when
//     the message already has real text, append a compact skip marker.
//   - A message with no text and no attachment content reconstructs to "" and is
//     skipped by the caller (its binaries are still counted for reporting).
func reconstructMessage(m *dumpMessage) (content string, skippedBinaries int) {
	skippedBinaries = len(m.Files)

	body := strings.TrimSpace(m.Text)
	if body == "" {
		body = joinTextBlocks(m.Content)
	}

	var parts []string
	if body != "" {
		parts = append(parts, body)
	}
	for _, a := range m.Attachments {
		c := strings.TrimSpace(a.ExtractedContent)
		if c == "" {
			continue
		}
		name := strings.TrimSpace(a.FileName)
		if name == "" {
			name = "(unnamed)"
		}
		header := "--- attachment: " + name
		if a.FileType != "" {
			header += " (" + a.FileType + ")"
		}
		header += " ---"
		parts = append(parts, header+"\n"+c)
	}

	content = strings.TrimSpace(strings.Join(parts, "\n\n"))
	if content == "" {
		// Fully empty message: skipped by the caller. Do not emit lone binary markers.
		return "", skippedBinaries
	}

	if len(m.Files) > 0 {
		markers := make([]string, 0, len(m.Files))
		for _, f := range m.Files {
			markers = append(markers, "[binary file skipped: "+f.FileName+"]")
		}
		content = content + "\n\n" + strings.Join(markers, "\n")
	}
	return content, skippedBinaries
}

// joinTextBlocks concatenates only the text of type=="text" content blocks.
func joinTextBlocks(blocks []dumpContentBlock) string {
	var parts []string
	for _, b := range blocks {
		if b.Type != "text" {
			continue
		}
		if t := strings.TrimSpace(b.Text); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n\n")
}

package craft

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ReadSkill reads document content from Craft.
type ReadSkill struct {
	client *Client
}

func NewRead(client *Client) *ReadSkill {
	return &ReadSkill{client: client}
}

func (s *ReadSkill) Name() string { return "craft_read" }

func (s *ReadSkill) Description() string {
	return "Read the full content of a Craft document by its ID. Use craft_search first to find the document ID. Can also list folders and documents."
}

func (s *ReadSkill) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"document_id": {
				"type": "string",
				"description": "Document ID to read (get it from craft_search)"
			},
			"list_folders": {
				"type": "boolean",
				"description": "If true, list all folders instead of reading a document"
			},
			"list_documents": {
				"type": "string",
				"description": "Folder ID to list documents from (empty = all unsorted)"
			}
		}
	}`)
}

func (s *ReadSkill) RequiredCapabilities() []string {
	return []string{"craft"}
}

type readInput struct {
	DocumentID    string `json:"document_id"`
	ListFolders   bool   `json:"list_folders"`
	ListDocuments string `json:"list_documents"`
}

func (s *ReadSkill) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in readInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	if in.ListFolders {
		return s.listFolders(ctx)
	}

	if in.ListDocuments != "" || (in.DocumentID == "" && !in.ListFolders) {
		return s.listDocuments(ctx, in.ListDocuments)
	}

	return s.readDocument(ctx, in.DocumentID)
}

func (s *ReadSkill) listFolders(ctx context.Context) (string, error) {
	folders, err := s.client.ListFolders(ctx)
	if err != nil {
		return "", fmt.Errorf("listing folders: %w", err)
	}

	if len(folders) == 0 {
		return "No folders found.", nil
	}

	var b strings.Builder
	b.WriteString("Folders:\n\n")
	for _, f := range folders {
		writeFolderTree(&b, f, 0)
	}
	return b.String(), nil
}

func writeFolderTree(b *strings.Builder, f Folder, depth int) {
	indent := strings.Repeat("  ", depth)
	fmt.Fprintf(b, "%s- **%s** (id: %s, %d docs)\n", indent, f.Name, f.ID, f.DocumentCount)
	for _, sub := range f.Subfolders {
		writeFolderTree(b, sub, depth+1)
	}
}

func (s *ReadSkill) listDocuments(ctx context.Context, folderID string) (string, error) {
	docs, err := s.client.ListDocuments(ctx, folderID)
	if err != nil {
		return "", fmt.Errorf("listing documents: %w", err)
	}

	if len(docs) == 0 {
		return "No documents found.", nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Documents (%d):\n\n", len(docs))
	for i, d := range docs {
		fmt.Fprintf(&b, "%d. **%s** (id: %s)\n", i+1, d.Title, d.ID)
	}
	return b.String(), nil
}

func (s *ReadSkill) readDocument(ctx context.Context, documentID string) (string, error) {
	content, err := s.client.ReadDocument(ctx, documentID)
	if err != nil {
		return "", fmt.Errorf("reading document: %w", err)
	}

	if content == "" {
		return "Document is empty.", nil
	}

	// Truncate very long documents.
	const maxLen = 12000
	if len(content) > maxLen {
		content = content[:maxLen] + "\n\n...(truncated)"
	}

	return fmt.Sprintf("<<<CRAFT_DOCUMENT>>>\n%s\n<<<END_CRAFT_DOCUMENT>>>", content), nil
}

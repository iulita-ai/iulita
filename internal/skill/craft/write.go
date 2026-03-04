package craft

import (
	"context"
	"encoding/json"
	"fmt"
)

// WriteSkill creates or appends content to Craft documents.
type WriteSkill struct {
	client *Client
}

func NewWrite(client *Client) *WriteSkill {
	return &WriteSkill{client: client}
}

func (s *WriteSkill) Name() string { return "craft_write" }

func (s *WriteSkill) Description() string {
	return "Create a new Craft document or append content to an existing one. Accepts markdown content."
}

func (s *WriteSkill) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"content": {
				"type": "string",
				"description": "Markdown content to write"
			},
			"document_id": {
				"type": "string",
				"description": "Existing document ID to append to. If empty, creates a new document."
			},
			"folder_id": {
				"type": "string",
				"description": "Folder ID for the new document (only used when creating)"
			}
		},
		"required": ["content"]
	}`)
}

func (s *WriteSkill) RequiredCapabilities() []string {
	return []string{"craft"}
}

type writeInput struct {
	Content    string `json:"content"`
	DocumentID string `json:"document_id"`
	FolderID   string `json:"folder_id"`
}

func (s *WriteSkill) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in writeInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	if in.Content == "" {
		return "", fmt.Errorf("content is required")
	}

	// Append to existing document.
	if in.DocumentID != "" {
		if err := s.client.AppendToDocument(ctx, in.DocumentID, in.Content); err != nil {
			return "", fmt.Errorf("appending to document: %w", err)
		}
		return fmt.Sprintf("Content appended to document %s.", in.DocumentID), nil
	}

	// Create new document.
	docID, err := s.client.CreateDocument(ctx, "", in.Content, in.FolderID)
	if err != nil {
		return "", fmt.Errorf("creating document: %w", err)
	}
	return fmt.Sprintf("Document created (id: %s).", docID), nil
}

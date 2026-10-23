package httpfetch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	engine "github.com/iulita-ai/iulita/internal/httpfetch"
	"github.com/iulita-ai/iulita/internal/skill"
)

// Attachment stages only host-provided attachments, never model-provided paths,
// URLs or bytes. External transmission remains subject to http_request policy.
type Attachment struct{ factory *engine.Factory }

// NewAttachment creates the current-message upload adapter.
func NewAttachment(factory *engine.Factory) *Attachment { return &Attachment{factory: factory} }

// Name returns the tool name.
func (s *Attachment) Name() string { return "http_attachment" }

// Description explains local staging and external upload semantics.
func (s *Attachment) Description() string {
	return "Prepare an attachment from the current user message for HTTP upload. Returns a private artifact reference, filename, size and hash; never file bytes. This only stores the file locally. To send it to the destination explicitly requested by the user, use http_request with body type artifact or multipart and artifact_ref. For JSON APIs such as Transmission torrent-add, use an exact {\"$artifact_base64\":\"artifact_ref\"} object at the binary field (for example arguments.metainfo); the engine inserts base64 bytes privately. Do not invent references, encode attachment bytes yourself, or claim an upload succeeded before the server confirms it. If no upload destination was requested, ask the user what to do with the attachment."
}

// InputSchema accepts only an index into trusted current attachments.
func (s *Attachment) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"required":["attachment_index"],"properties":{"attachment_index":{"type":"integer","minimum":0,"description":"Zero-based document attachment index in the current message"}}}`)
}

// Execute stages one attachment without revealing its bytes.
func (s *Attachment) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var input struct {
		Index *int `json:"attachment_index"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&input); err != nil {
		return "", fmt.Errorf("invalid attachment input: %w", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return "", fmt.Errorf("expected a single attachment input")
	}
	docs := skill.DocumentsFrom(ctx)
	if input.Index == nil || *input.Index < 0 || *input.Index >= len(docs) {
		return "", fmt.Errorf("attachment_index must refer to a document in the current message")
	}
	scope, ok := engine.ScopeFrom(ctx)
	if !ok {
		scope = engine.Scope{UserID: skill.UserIDFrom(ctx), ChatID: skill.ChatIDFrom(ctx), Mode: engine.ModeInteractive}
	}
	doc := docs[*input.Index]
	view, err := s.factory.StageAttachment(engine.WithScope(ctx, scope), doc.Data, doc.Filename, doc.MimeType)
	if err != nil {
		return "", err
	}
	out, err := json.Marshal(struct {
		ArtifactRef string              `json:"artifact_ref"`
		Artifact    engine.ArtifactView `json:"artifact"`
		Uploaded    bool                `json:"uploaded"`
	}{view.ID, view, false})
	return string(out), err
}

// SuppressResultPreview keeps private file metadata out of generic logs.
func (s *Attachment) SuppressResultPreview() bool { return true }

// IsSensitiveOutput marks private attachment metadata as sensitive.
func (s *Attachment) IsSensitiveOutput() bool { return true }

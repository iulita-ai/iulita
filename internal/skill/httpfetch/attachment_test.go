package httpfetch

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iulita-ai/iulita/internal/skill"
)

func TestAttachmentRejectsModelSuppliedSources(t *testing.T) {
	s := NewAttachment(nil)
	ctx := skill.WithDocuments(context.Background(), []skill.DocumentAttachment{{Data: []byte("private")}})
	for _, raw := range []string{`{}`, `{"attachment_index":-1}`, `{"attachment_index":1}`, `{"attachment_index":0,"path":"/secret"}`, `{"attachment_index":0,"data":"private"}`, `{"attachment_index":0} {}`, `null`} {
		if result, err := s.Execute(ctx, json.RawMessage(raw)); err == nil || strings.Contains(result, "private") {
			t.Fatalf("accepted %s: %s %v", raw, result, err)
		}
	}
}

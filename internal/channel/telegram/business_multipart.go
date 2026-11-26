package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"strings"

	business "github.com/iulita-ai/iulita/internal/telegrambusiness"
)

// Attachments are materialized only after an approved action has been claimed.
// Profile/story InputFile values must be new, scope-bound uploads, never a URL
// or a guessed filesystem path or a JSON-encoded fake Telegram file reference.
func (c *Channel) businessMultipartCall(ctx context.Context, request business.Request, payload map[string]any, result any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return business.ErrDenied
	}
	var values map[string]any
	if json.Unmarshal(data, &values) != nil {
		return business.ErrDenied
	}
	files := map[string]business.MediaBlob{}
	var visit func(map[string]any, int) error
	visit = func(object map[string]any, depth int) error {
		if depth > 8 {
			return business.ErrDenied
		}
		for key, value := range object {
			if nested, ok := value.(map[string]any); ok {
				if checkErr := visit(nested, depth+1); checkErr != nil {
					return checkErr
				}
				continue
			}
			text, ok := value.(string)
			if !ok || !strings.HasPrefix(text, "bmedia_") {
				continue
			}
			switch key {
			case "photo", "document", "voice", "audio", "video", "animation":
			default:
				continue
			}
			if len(files) >= 3 || c.business == nil {
				return business.ErrDenied
			}
			blob, err := c.business.ReadMedia(ctx, request.Action.Scope, text)
			if err != nil {
				return err
			}
			name := fmt.Sprintf("bfile%d", len(files))
			files[name] = blob
			object[key] = "attach://" + name
		}
		return nil
	}
	if checkErr := visit(values, 0); checkErr != nil {
		return checkErr
	}
	if len(files) == 0 {
		return c.businessCall(ctx, request.MethodName(), payload, result)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range values {
		text, ok := value.(string)
		if !ok {
			encoded, err := json.Marshal(value)
			if err != nil {
				return business.ErrDenied
			}
			text = string(encoded)
		}
		if writer.WriteField(key, text) != nil {
			return business.ErrUnavailable
		}
	}
	for name := range files {
		blob := files[name]
		part, err := writer.CreateFormFile(name, blob.Filename)
		if err != nil {
			return business.ErrUnavailable
		}
		if _, err := part.Write(blob.Data); err != nil {
			return business.ErrUnavailable
		}
	}
	if writer.Close() != nil {
		return business.ErrUnavailable
	}
	if body.Len() > 4<<20 {
		return business.ErrQuota
	}
	return c.businessWire(ctx, request.MethodName(), writer.FormDataContentType(), &body, result)
}

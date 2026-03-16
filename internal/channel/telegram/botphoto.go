package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"

	"go.uber.org/zap"
)

// SetBotPhoto uploads a new profile photo for the bot.
// data must be JPEG or PNG bytes, max 5 MB.
func (c *Channel) SetBotPhoto(_ context.Context, data []byte) error {
	c.logger.Info("SetBotPhoto called", zap.Int("data_len", len(data)))

	// Telegram Bot API 9.4: setMyProfilePhoto expects "photo" as an InputProfilePhoto
	// JSON object. For a static photo, the structure is:
	//   photo = {"type": "static", "photo": "attach://photo_file"}
	// and the actual file is sent as a multipart field named "photo_file".
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	// 1. Add the "photo" field as JSON describing the InputProfilePhotoStatic.
	if err := w.WriteField("photo", `{"type":"static","photo":"attach://photo_file"}`); err != nil {
		return fmt.Errorf("setMyProfilePhoto: writing photo field: %w", err)
	}

	// 2. Add the actual image file as "photo_file" multipart part.
	partHeader := make(textproto.MIMEHeader)
	partHeader.Set("Content-Disposition", `form-data; name="photo_file"; filename="photo.png"`)
	partHeader.Set("Content-Type", "image/png")
	part, err := w.CreatePart(partHeader)
	if err != nil {
		return fmt.Errorf("setMyProfilePhoto: creating form file: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return fmt.Errorf("setMyProfilePhoto: writing data: %w", err)
	}
	w.Close()

	c.logger.Info("SetBotPhoto multipart built",
		zap.Int("total_body_size", buf.Len()),
		zap.String("content_type", w.FormDataContentType()))

	url := fmt.Sprintf("https://api.telegram.org/bot%s/setMyProfilePhoto", c.bot.Token)
	req, err := http.NewRequest(http.MethodPost, url, &buf)
	if err != nil {
		return fmt.Errorf("setMyProfilePhoto: creating request: %w", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("setMyProfilePhoto: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	c.logger.Info("SetBotPhoto response",
		zap.Int("status", resp.StatusCode),
		zap.String("body", string(body)))

	var result struct {
		Ok          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("setMyProfilePhoto: status %d, body: %s", resp.StatusCode, string(body))
	}
	if !result.Ok {
		return fmt.Errorf("setMyProfilePhoto: %s", result.Description)
	}
	return nil
}

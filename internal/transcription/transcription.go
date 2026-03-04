// Package transcription provides audio transcription via external APIs.
package transcription

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
)

// Provider transcribes audio data to text.
type Provider interface {
	Transcribe(ctx context.Context, audio []byte, format string) (string, error)
}

// OpenAI implements Provider using OpenAI's Whisper API.
type OpenAI struct {
	apiKey     string
	model      string
	httpClient *http.Client
	baseURL    string
}

// NewOpenAI creates a new OpenAI Whisper transcription provider.
func NewOpenAI(apiKey, model string, httpClient *http.Client) *OpenAI {
	if model == "" {
		model = "whisper-1"
	}
	return &OpenAI{
		apiKey:     apiKey,
		model:      model,
		httpClient: httpClient,
		baseURL:    "https://api.openai.com/v1",
	}
}

func (o *OpenAI) Transcribe(ctx context.Context, audio []byte, format string) (string, error) {
	if format == "" {
		format = "ogg"
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	part, err := writer.CreateFormFile("file", "audio."+format)
	if err != nil {
		return "", fmt.Errorf("creating form file: %w", err)
	}
	if _, err := part.Write(audio); err != nil {
		return "", fmt.Errorf("writing audio data: %w", err)
	}

	if err := writer.WriteField("model", o.model); err != nil {
		return "", fmt.Errorf("writing model field: %w", err)
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("closing multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/audio/transcriptions", &body)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+o.apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("transcription request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("transcription API error %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parsing response: %w", err)
	}

	return result.Text, nil
}

package notify

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// NtfyNotifier sends push notifications via an ntfy server.
type NtfyNotifier struct {
	url    string
	token  string
	client *http.Client
}

// NewNtfy creates a new NtfyNotifier with the given server URL and optional auth token.
func NewNtfy(url, token string) *NtfyNotifier {
	return &NtfyNotifier{
		url:    url,
		token:  token,
		client: &http.Client{},
	}
}

// Send sends a push notification via ntfy.
func (n *NtfyNotifier) Send(ctx context.Context, title, message string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.url, strings.NewReader(message))
	if err != nil {
		return fmt.Errorf("ntfy: create request: %w", err)
	}
	req.Header.Set("Title", title)
	if n.token != "" {
		req.Header.Set("Authorization", "Bearer "+n.token)
	}

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("ntfy: send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ntfy: unexpected status %d", resp.StatusCode)
	}
	return nil
}

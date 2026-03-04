package notify

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const pushoverAPIURL = "https://api.pushover.net/1/messages.json"

// PushoverNotifier sends push notifications via the Pushover HTTP API.
type PushoverNotifier struct {
	token   string
	userKey string
	client  *http.Client
}

// NewPushover creates a new PushoverNotifier with the given API token and user key.
func NewPushover(token, userKey string) *PushoverNotifier {
	return &PushoverNotifier{
		token:   token,
		userKey: userKey,
		client:  &http.Client{},
	}
}

// Send sends a push notification via Pushover.
func (p *PushoverNotifier) Send(ctx context.Context, title, message string) error {
	form := url.Values{
		"token":   {p.token},
		"user":    {p.userKey},
		"title":   {title},
		"message": {message},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, pushoverAPIURL, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("pushover: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("pushover: send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pushover: unexpected status %d", resp.StatusCode)
	}
	return nil
}

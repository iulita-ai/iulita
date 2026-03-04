package google

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"google.golang.org/api/gmail/v1"

	"github.com/iulita-ai/iulita/internal/skill"
)

// CapabilityAdder allows adding/removing capabilities at runtime.
type CapabilityAdder interface {
	AddCapability(cap string)
	RemoveCapability(cap string)
}

// ConfigReader reads effective config values.
type ConfigReader interface {
	GetEffective(key string) (string, bool)
}

// MailSkill reads and searches Gmail messages.
type MailSkill struct {
	client   *Client
	capAdder CapabilityAdder // optional, for hot-reload
	cfgRead  ConfigReader    // optional, for hot-reload
}

func NewMail(client *Client) *MailSkill {
	return &MailSkill{client: client}
}

// SetReloader configures hot-reload support for credential changes.
func (s *MailSkill) SetReloader(ca CapabilityAdder, cr ConfigReader) {
	s.capAdder = ca
	s.cfgRead = cr
}

// OnConfigChanged implements skill.ConfigReloadable.
func (s *MailSkill) OnConfigChanged(key, value string) {
	if s.capAdder == nil || s.cfgRead == nil {
		return
	}
	clientID, _ := s.cfgRead.GetEffective("skills.google.client_id")
	clientSecret, _ := s.cfgRead.GetEffective("skills.google.client_secret")
	if clientID != "" && clientSecret != "" {
		redirectURL, _ := s.cfgRead.GetEffective("skills.google.redirect_url")
		s.client.UpdateOAuthConfig(clientID, clientSecret, redirectURL)
		s.capAdder.AddCapability("google")
	}
	if credFile, ok := s.cfgRead.GetEffective("skills.google.credentials_file"); ok {
		s.client.SetCredentialsFile(credFile)
	}
	if scopes, ok := s.cfgRead.GetEffective("skills.google.scopes"); ok && scopes != "" {
		s.client.SetScopes(ParseScopesConfig(scopes))
	}
}

func (s *MailSkill) Name() string { return "google_mail" }

func (s *MailSkill) Description() string {
	return "Read and search Gmail messages. Supports listing unread, searching with Gmail query syntax, reading a specific message, and getting an unread summary. Does NOT mark messages as read."
}

func (s *MailSkill) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["unread", "search", "read", "summary"],
				"description": "Action: unread (list unread), search (query), read (single message), summary (unread count + top subjects)"
			},
			"query": {
				"type": "string",
				"description": "Gmail search query for 'search' action (e.g. 'from:boss subject:urgent')"
			},
			"message_id": {
				"type": "string",
				"description": "Message ID for 'read' action"
			},
			"limit": {
				"type": "integer",
				"description": "Max messages to return (default 10, max 50)"
			},
			"account": {
				"type": "string",
				"description": "Google account alias or email (default: primary)"
			}
		},
		"required": ["action"]
	}`)
}

func (s *MailSkill) RequiredCapabilities() []string { return []string{"google"} }

type mailInput struct {
	Action    string `json:"action"`
	Query     string `json:"query"`
	MessageID string `json:"message_id"`
	Limit     int64  `json:"limit"`
	Account   string `json:"account"`
}

func (s *MailSkill) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in mailInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	userID := skill.UserIDFrom(ctx)
	if userID == "" {
		return "", fmt.Errorf("user not identified")
	}

	if !s.client.HasAccounts(ctx, userID) {
		return "No Google account connected. Please connect one in Settings.", nil
	}

	srv, err := s.client.GetGmailService(ctx, userID, in.Account)
	if err != nil {
		return "", fmt.Errorf("creating Gmail service: %w", err)
	}

	switch in.Action {
	case "unread":
		return s.listMessages(srv, "is:unread", in.Limit)
	case "search":
		if in.Query == "" {
			return "", fmt.Errorf("query is required for search action")
		}
		return s.listMessages(srv, in.Query, in.Limit)
	case "read":
		if in.MessageID == "" {
			return "", fmt.Errorf("message_id is required for read action")
		}
		return s.readMessage(srv, in.MessageID)
	case "summary":
		return s.unreadSummary(srv)
	default:
		return "", fmt.Errorf("unknown action %q (use: unread, search, read, summary)", in.Action)
	}
}

func (s *MailSkill) listMessages(srv *gmail.Service, query string, limit int64) (string, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}

	resp, err := srv.Users.Messages.List("me").Q(query).MaxResults(limit).Do()
	if err != nil {
		return "", fmt.Errorf("listing messages: %w", err)
	}

	if len(resp.Messages) == 0 {
		return "No messages found.", nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Found %d message(s):\n\n", len(resp.Messages))

	for i, m := range resp.Messages {
		msg, err := srv.Users.Messages.Get("me", m.Id).Format("metadata").
			MetadataHeaders("From", "Subject", "Date").Do()
		if err != nil {
			fmt.Fprintf(&b, "%d. [error reading message %s]\n", i+1, m.Id)
			continue
		}

		from, subject, date := "", "", ""
		for _, h := range msg.Payload.Headers {
			switch h.Name {
			case "From":
				from = h.Value
			case "Subject":
				subject = h.Value
			case "Date":
				date = h.Value
			}
		}

		fmt.Fprintf(&b, "%d. **%s**\n   From: %s\n   Date: %s\n   Snippet: %s\n   [id: %s]\n\n",
			i+1, subject, from, date, msg.Snippet, m.Id)
	}

	return b.String(), nil
}

func (s *MailSkill) readMessage(srv *gmail.Service, messageID string) (string, error) {
	msg, err := srv.Users.Messages.Get("me", messageID).Format("full").Do()
	if err != nil {
		return "", fmt.Errorf("reading message: %w", err)
	}

	from, subject, date, to := "", "", "", ""
	for _, h := range msg.Payload.Headers {
		switch h.Name {
		case "From":
			from = h.Value
		case "Subject":
			subject = h.Value
		case "Date":
			date = h.Value
		case "To":
			to = h.Value
		}
	}

	body := extractBody(msg.Payload)
	if len(body) > 4000 {
		body = body[:4000] + "\n... [truncated]"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "**%s**\nFrom: %s\nTo: %s\nDate: %s\n\n%s", subject, from, to, date, body)
	return b.String(), nil
}

func (s *MailSkill) unreadSummary(srv *gmail.Service) (string, error) {
	resp, err := srv.Users.Messages.List("me").Q("is:unread").MaxResults(50).Do()
	if err != nil {
		return "", fmt.Errorf("listing unread: %w", err)
	}

	total := resp.ResultSizeEstimate
	if total == 0 {
		return "No unread messages.", nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Unread messages: ~%d\n\nTop subjects:\n", total)

	limit := 5
	if len(resp.Messages) < limit {
		limit = len(resp.Messages)
	}

	for i := 0; i < limit; i++ {
		msg, err := srv.Users.Messages.Get("me", resp.Messages[i].Id).Format("metadata").
			MetadataHeaders("From", "Subject").Do()
		if err != nil {
			continue
		}
		from, subject := "", ""
		for _, h := range msg.Payload.Headers {
			switch h.Name {
			case "From":
				from = h.Value
			case "Subject":
				subject = h.Value
			}
		}
		fmt.Fprintf(&b, "%d. %s — %s\n", i+1, subject, from)
	}

	return b.String(), nil
}

// extractBody extracts text/plain body from MIME payload, falling back to text/html.
func extractBody(payload *gmail.MessagePart) string {
	if payload == nil {
		return ""
	}

	if payload.MimeType == "text/plain" && payload.Body != nil && payload.Body.Data != "" {
		data, err := base64.URLEncoding.DecodeString(payload.Body.Data)
		if err == nil {
			return string(data)
		}
	}

	// Try multipart parts.
	var htmlBody string
	for _, part := range payload.Parts {
		if part.MimeType == "text/plain" && part.Body != nil && part.Body.Data != "" {
			data, err := base64.URLEncoding.DecodeString(part.Body.Data)
			if err == nil {
				return string(data)
			}
		}
		if part.MimeType == "text/html" && part.Body != nil && part.Body.Data != "" {
			data, err := base64.URLEncoding.DecodeString(part.Body.Data)
			if err == nil {
				htmlBody = string(data)
			}
		}
		// Recurse into nested multipart.
		if len(part.Parts) > 0 {
			if body := extractBody(part); body != "" {
				return body
			}
		}
	}

	if htmlBody != "" {
		return "[HTML content — request plain text view if needed]\n" + htmlBody
	}

	if payload.Body != nil && payload.Body.Data != "" {
		data, err := base64.URLEncoding.DecodeString(payload.Body.Data)
		if err == nil {
			return string(data)
		}
	}
	return ""
}

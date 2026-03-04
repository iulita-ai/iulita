package craft

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// CapabilityAdder allows adding/removing capabilities at runtime (satisfied by *skill.Registry).
type CapabilityAdder interface {
	AddCapability(cap string)
	RemoveCapability(cap string)
}

// ConfigReader reads effective config values (satisfied by *config.Store).
type ConfigReader interface {
	GetEffective(key string) (string, bool)
}

// SearchSkill searches documents in Craft.
type SearchSkill struct {
	client   *Client
	capAdder CapabilityAdder // optional, for hot-reload
	cfgRead  ConfigReader    // optional, for hot-reload
}

func NewSearch(client *Client) *SearchSkill {
	return &SearchSkill{client: client}
}

// SetReloader configures hot-reload support for credential changes.
func (s *SearchSkill) SetReloader(ca CapabilityAdder, cr ConfigReader) {
	s.capAdder = ca
	s.cfgRead = cr
}

// OnConfigChanged implements skill.ConfigReloadable.
func (s *SearchSkill) OnConfigChanged(key, value string) {
	if s.capAdder == nil || s.cfgRead == nil {
		return
	}
	apiURL, _ := s.cfgRead.GetEffective("skills.craft.api_url")
	apiKey, _ := s.cfgRead.GetEffective("skills.craft.api_key")
	if apiURL != "" && apiKey != "" {
		s.client.UpdateCredentials(apiURL, apiKey)
		s.capAdder.AddCapability("craft")
	} else {
		s.capAdder.RemoveCapability("craft")
	}
}

func (s *SearchSkill) Name() string { return "craft_search" }

func (s *SearchSkill) Description() string {
	return "Search documents in Craft by content or title. Returns matching document titles and excerpts."
}

func (s *SearchSkill) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {
				"type": "string",
				"description": "Search query to find documents"
			}
		},
		"required": ["query"]
	}`)
}

func (s *SearchSkill) RequiredCapabilities() []string {
	return []string{"craft"}
}

type searchInput struct {
	Query string `json:"query"`
}

func (s *SearchSkill) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in searchInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	if in.Query == "" {
		return "", fmt.Errorf("query is required")
	}

	results, err := s.client.SearchDocuments(ctx, in.Query)
	if err != nil {
		return "", fmt.Errorf("search failed: %w", err)
	}

	if len(results) == 0 {
		return "No documents found matching the query.", nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Found %d result(s):\n\n", len(results))
	for i, r := range results {
		fmt.Fprintf(&b, "%d. [doc: %s] %s\n\n", i+1, r.DocumentID, r.Markdown)
	}
	return b.String(), nil
}

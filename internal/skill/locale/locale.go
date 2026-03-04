package locale

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"strings"

	"golang.org/x/text/language"

	"github.com/iulita-ai/iulita/internal/i18n"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/storage"
)

//go:embed SKILL.md
var skillFS embed.FS

// LoadManifest loads the locale skill manifest from embedded SKILL.md.
func LoadManifest() (*skill.Manifest, error) {
	return skill.LoadManifestFromFS(skillFS, "SKILL.md")
}

// Skill allows users to change the language of their channel via chat.
type Skill struct {
	store storage.Repository
}

// New creates a locale switching skill.
func New(store storage.Repository) *Skill {
	return &Skill{store: store}
}

func (s *Skill) Name() string        { return "set_language" }
func (s *Skill) Description() string { return "Change the interface language for this channel" }

func (s *Skill) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"language": {
				"type": "string",
				"description": "Language code or name (e.g. 'en', 'ru', 'zh', 'es', 'fr', 'he', or 'English', 'Russian', 'Chinese', 'Spanish', 'French', 'Hebrew')"
			}
		},
		"required": ["language"]
	}`)
}

func (s *Skill) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var params struct {
		Language string `json:"language"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return "", fmt.Errorf("parsing input: %w", err)
	}

	tag := resolveTag(params.Language)
	if tag == language.Und {
		return i18n.T(ctx, "LocaleUnknown", map[string]any{"Language": params.Language}), nil
	}

	chatID := skill.ChatIDFrom(ctx)
	if chatID == "" {
		return "no chat context", nil
	}

	locale := i18n.TagString(tag)
	if err := s.store.UpdateChannelLocale(ctx, chatID, locale); err != nil {
		return "", fmt.Errorf("updating locale: %w", err)
	}

	langName := i18n.LanguageName(tag)
	return i18n.Tl(tag, "LocaleSwitched", map[string]any{"Language": langName}), nil
}

// resolveTag converts a language name or code to a language.Tag.
func resolveTag(input string) language.Tag {
	lower := strings.ToLower(strings.TrimSpace(input))

	// Map common names to tags.
	nameMap := map[string]language.Tag{
		"english":  language.English,
		"russian":  language.Russian,
		"chinese":  language.Chinese,
		"mandarin": language.Chinese,
		"spanish":  language.Spanish,
		"french":   language.French,
		"hebrew":   language.Hebrew,
		// Russian language names
		"английский":  language.English,
		"русский":     language.Russian,
		"китайский":   language.Chinese,
		"испанский":   language.Spanish,
		"французский": language.French,
		"иврит":       language.Hebrew,
	}

	if tag, ok := nameMap[lower]; ok {
		return tag
	}

	// Try parsing as BCP-47 tag.
	tag, err := language.Parse(lower)
	if err != nil {
		return language.Und
	}

	if i18n.IsSupported(lower) {
		return tag
	}
	return language.Und
}

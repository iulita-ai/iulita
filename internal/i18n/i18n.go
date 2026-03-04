package i18n

import (
	"embed"
	"fmt"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
	"github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

//go:embed catalog/*.toml
var catalogFS embed.FS

// Bundle wraps the go-i18n bundle and provides a simple translation API.
type Bundle struct {
	bundle     *i18n.Bundle
	localizers map[string]*i18n.Localizer // cached per tag string

	// Pre-parsed approval words per locale for O(1) lookup.
	mu               sync.RWMutex
	affirmativeWords map[string]map[string]bool // tag → word set
	negativeWords    map[string]map[string]bool
}

// defaultBundle is the package-level bundle initialized by Init.
var defaultBundle *Bundle

// Init loads all embedded catalog files and initializes the default bundle.
func Init() error {
	b, err := NewBundle()
	if err != nil {
		return err
	}
	defaultBundle = b
	return nil
}

// NewBundle creates a new i18n bundle from embedded TOML catalogs.
func NewBundle() (*Bundle, error) {
	bnd := i18n.NewBundle(language.English)
	bnd.RegisterUnmarshalFunc("toml", toml.Unmarshal)

	entries, err := catalogFS.ReadDir("catalog")
	if err != nil {
		return nil, fmt.Errorf("reading catalog dir: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".toml") {
			continue
		}
		data, err := catalogFS.ReadFile("catalog/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", entry.Name(), err)
		}
		if _, err := bnd.ParseMessageFileBytes(data, entry.Name()); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", entry.Name(), err)
		}
	}

	b := &Bundle{
		bundle:           bnd,
		localizers:       make(map[string]*i18n.Localizer, len(SupportedLanguages)),
		affirmativeWords: make(map[string]map[string]bool),
		negativeWords:    make(map[string]map[string]bool),
	}
	// Pre-cache localizers for all supported languages.
	for _, tag := range SupportedLanguages {
		ts := TagString(tag)
		b.localizers[ts] = i18n.NewLocalizer(bnd, ts, "en")
	}
	b.parseApprovalWords()
	return b, nil
}

// T translates a message ID using the locale from context.
// Falls back to English, then returns the message ID itself.
func T(ctx interface{ Value(any) any }, msgID string, data ...map[string]any) string {
	if defaultBundle == nil {
		return msgID
	}
	tag := language.English
	if v, ok := ctx.Value(localeKey{}).(language.Tag); ok {
		tag = v
	}
	return defaultBundle.Translate(tag, msgID, data...)
}

// Tl translates a message ID using a specific locale tag.
func Tl(tag language.Tag, msgID string, data ...map[string]any) string {
	if defaultBundle == nil {
		return msgID
	}
	return defaultBundle.Translate(tag, msgID, data...)
}

// Translate performs the actual translation.
func (b *Bundle) Translate(tag language.Tag, msgID string, data ...map[string]any) string {
	ts := TagString(tag)
	loc, ok := b.localizers[ts]
	if !ok {
		loc = i18n.NewLocalizer(b.bundle, ts, "en")
	}

	cfg := &i18n.LocalizeConfig{MessageID: msgID}
	if len(data) > 0 && data[0] != nil {
		cfg.TemplateData = data[0]
	}

	result, err := loc.Localize(cfg)
	if err != nil || result == "" {
		return msgID
	}
	return result
}

// parseApprovalWords pre-parses the comma-separated approval word lists
// from the catalog for all supported languages.
func (b *Bundle) parseApprovalWords() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, tag := range SupportedLanguages {
		ts := TagString(tag)
		b.affirmativeWords[ts] = parseWordList(b.Translate(tag, "ApprovalAffirmative"))
		b.negativeWords[ts] = parseWordList(b.Translate(tag, "ApprovalNegative"))
	}
}

// IsApprovalAffirmative checks if text is an affirmative approval word for the given locale.
func (b *Bundle) IsApprovalAffirmative(tag language.Tag, text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	b.mu.RLock()
	defer b.mu.RUnlock()
	ts := TagString(tag)
	if words, ok := b.affirmativeWords[ts]; ok {
		return words[lower]
	}
	// Fallback to English.
	if words, ok := b.affirmativeWords["en"]; ok {
		return words[lower]
	}
	return false
}

// IsApprovalNegative checks if text is a negative approval word for the given locale.
func (b *Bundle) IsApprovalNegative(tag language.Tag, text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	b.mu.RLock()
	defer b.mu.RUnlock()
	ts := TagString(tag)
	if words, ok := b.negativeWords[ts]; ok {
		return words[lower]
	}
	if words, ok := b.negativeWords["en"]; ok {
		return words[lower]
	}
	return false
}

// DefaultBundle returns the package-level bundle for direct access.
func DefaultBundle() *Bundle {
	return defaultBundle
}

func parseWordList(s string) map[string]bool {
	words := make(map[string]bool)
	for _, w := range strings.Split(s, ",") {
		w = strings.TrimSpace(strings.ToLower(w))
		if w != "" {
			words[w] = true
		}
	}
	return words
}

package i18n

import (
	"context"
	"testing"

	"golang.org/x/text/language"
)

func TestInit(t *testing.T) {
	if err := Init(); err != nil {
		t.Fatalf("Init() failed: %v", err)
	}
	if defaultBundle == nil {
		t.Fatal("defaultBundle is nil after Init()")
	}
}

func TestTranslateEnglish(t *testing.T) {
	if err := Init(); err != nil {
		t.Fatalf("Init() failed: %v", err)
	}

	tests := []struct {
		msgID    string
		expected string
	}{
		{"ApprovalCancelled", "Cancelled."},
		{"TelegramHistoryCleared", "History cleared."},
		{"ConsolePlaceholder", "Type a message..."},
		{"ConsoleThinking", "[thinking...]"},
		{"CommandHelpHeader", "Available commands:"},
		{"WeatherWMO0", "Clear sky"},
		{"WeatherWMO95", "Thunderstorm"},
	}

	ctx := WithLocale(context.Background(), language.English)
	for _, tt := range tests {
		got := T(ctx, tt.msgID)
		if got != tt.expected {
			t.Errorf("T(ctx, %q) = %q, want %q", tt.msgID, got, tt.expected)
		}
	}
}

func TestTranslateRussian(t *testing.T) {
	if err := Init(); err != nil {
		t.Fatalf("Init() failed: %v", err)
	}

	tests := []struct {
		msgID    string
		expected string
	}{
		{"ApprovalCancelled", "Отменено."},
		{"TelegramHistoryCleared", "История очищена."},
		{"ConsolePlaceholder", "Введите сообщение..."},
		{"WeatherWMO0", "Ясно"},
	}

	ctx := WithLocale(context.Background(), language.Russian)
	for _, tt := range tests {
		got := T(ctx, tt.msgID)
		if got != tt.expected {
			t.Errorf("T(ctx, %q) = %q, want %q", tt.msgID, got, tt.expected)
		}
	}
}

func TestTranslateWithData(t *testing.T) {
	if err := Init(); err != nil {
		t.Fatalf("Init() failed: %v", err)
	}

	ctx := WithLocale(context.Background(), language.English)
	got := T(ctx, "ConsoleCompressDone", map[string]any{"Count": 42})
	if got != "Compressed 42 messages into summary" {
		t.Errorf("T with data = %q", got)
	}
}

func TestTranslateFallback(t *testing.T) {
	if err := Init(); err != nil {
		t.Fatalf("Init() failed: %v", err)
	}

	ctx := WithLocale(context.Background(), language.English)
	got := T(ctx, "NonExistentKey")
	if got != "NonExistentKey" {
		t.Errorf("expected fallback to key, got %q", got)
	}
}

func TestResolveLocale(t *testing.T) {
	tests := []struct {
		channelLocale string
		msgLang       string
		want          language.Tag
	}{
		{"ru", "", language.Russian},
		{"", "ru", language.Russian},
		{"en", "ru", language.English}, // channel takes priority
		{"", "", language.English},     // default
		{"zh", "", language.Chinese},
		{"he", "", language.Hebrew},
		{"fr", "", language.French},
		{"es", "", language.Spanish},
		{"de", "", language.English}, // unsupported → falls back
	}

	for _, tt := range tests {
		got := ResolveLocale(tt.channelLocale, tt.msgLang)
		if got != tt.want {
			t.Errorf("ResolveLocale(%q, %q) = %v, want %v", tt.channelLocale, tt.msgLang, got, tt.want)
		}
	}
}

func TestLanguageName(t *testing.T) {
	tests := []struct {
		tag  language.Tag
		want string
	}{
		{language.English, "English"},
		{language.Russian, "Russian"},
		{language.Chinese, "Chinese"},
		{language.Spanish, "Spanish"},
		{language.French, "French"},
		{language.Hebrew, "Hebrew"},
	}

	for _, tt := range tests {
		got := LanguageName(tt.tag)
		if got != tt.want {
			t.Errorf("LanguageName(%v) = %q, want %q", tt.tag, got, tt.want)
		}
	}
}

func TestIsSupported(t *testing.T) {
	tests := []struct {
		locale string
		want   bool
	}{
		{"en", true},
		{"ru", true},
		{"zh", true},
		{"es", true},
		{"fr", true},
		{"he", true},
		{"de", false},
		{"ja", false},
		{"", false},
		{"invalid", false},
	}

	for _, tt := range tests {
		got := IsSupported(tt.locale)
		if got != tt.want {
			t.Errorf("IsSupported(%q) = %v, want %v", tt.locale, got, tt.want)
		}
	}
}

func TestApprovalWords(t *testing.T) {
	if err := Init(); err != nil {
		t.Fatalf("Init() failed: %v", err)
	}

	b := DefaultBundle()

	// English affirmative
	for _, w := range []string{"yes", "y", "confirm", "ok", "approve"} {
		if !b.IsApprovalAffirmative(language.English, w) {
			t.Errorf("expected %q to be affirmative (en)", w)
		}
	}

	// English negative
	for _, w := range []string{"no", "n", "cancel", "deny", "reject"} {
		if !b.IsApprovalNegative(language.English, w) {
			t.Errorf("expected %q to be negative (en)", w)
		}
	}

	// Russian affirmative
	for _, w := range []string{"да", "ок", "подтвердить"} {
		if !b.IsApprovalAffirmative(language.Russian, w) {
			t.Errorf("expected %q to be affirmative (ru)", w)
		}
	}

	// Russian negative
	for _, w := range []string{"нет", "отмена", "отменить"} {
		if !b.IsApprovalNegative(language.Russian, w) {
			t.Errorf("expected %q to be negative (ru)", w)
		}
	}

	// Hebrew affirmative
	for _, w := range []string{"כן", "אישור"} {
		if !b.IsApprovalAffirmative(language.Hebrew, w) {
			t.Errorf("expected %q to be affirmative (he)", w)
		}
	}

	// Chinese affirmative
	for _, w := range []string{"是", "好", "确认"} {
		if !b.IsApprovalAffirmative(language.Chinese, w) {
			t.Errorf("expected %q to be affirmative (zh)", w)
		}
	}

	// Spanish
	for _, w := range []string{"sí", "confirmar"} {
		if !b.IsApprovalAffirmative(language.Spanish, w) {
			t.Errorf("expected %q to be affirmative (es)", w)
		}
	}

	// French
	for _, w := range []string{"oui", "confirmer"} {
		if !b.IsApprovalAffirmative(language.French, w) {
			t.Errorf("expected %q to be affirmative (fr)", w)
		}
	}

	// Cross-language: EN words work in all locales (they're included in all catalogs)
	if !b.IsApprovalAffirmative(language.Russian, "yes") {
		t.Error("expected 'yes' to work in Russian locale (included in catalog)")
	}
}

func TestLocaleFromContext(t *testing.T) {
	// Default (no locale set)
	ctx := context.Background()
	got := LocaleFrom(ctx)
	if got != language.English {
		t.Errorf("LocaleFrom(empty ctx) = %v, want English", got)
	}

	// With locale
	ctx = WithLocale(ctx, language.Russian)
	got = LocaleFrom(ctx)
	if got != language.Russian {
		t.Errorf("LocaleFrom(ru ctx) = %v, want Russian", got)
	}
}

func TestTl(t *testing.T) {
	if err := Init(); err != nil {
		t.Fatalf("Init() failed: %v", err)
	}

	got := Tl(language.French, "ApprovalCancelled")
	if got != "Annulé." {
		t.Errorf("Tl(French, ApprovalCancelled) = %q, want %q", got, "Annulé.")
	}
}

func TestAllLocalesHaveAllKeys(t *testing.T) {
	if err := Init(); err != nil {
		t.Fatalf("Init() failed: %v", err)
	}

	// Core keys that must exist in all locales.
	keys := []string{
		"ApprovalAffirmative",
		"ApprovalNegative",
		"ApprovalCancelled",
		"TelegramHistoryCleared",
		"ConsolePlaceholder",
		"ConsoleThinking",
		"CommandHelpHeader",
		"WeatherWMO0",
		"LocaleSwitched",
		"AssistantLanguageDirective",
	}

	for _, tag := range SupportedLanguages {
		for _, key := range keys {
			got := Tl(tag, key)
			if got == key {
				t.Errorf("missing translation for key %q in locale %v", key, tag)
			}
		}
	}
}

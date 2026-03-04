package i18n

import "golang.org/x/text/language"

// SupportedLanguages lists the BCP-47 tags this application supports.
var SupportedLanguages = []language.Tag{
	language.English,
	language.Russian,
	language.Chinese,
	language.Spanish,
	language.French,
	language.Hebrew,
}

var matcher = language.NewMatcher(SupportedLanguages)

// ResolveLocale determines the best locale from channel-stored locale and
// the incoming message's language code. Priority: channelLocale > msgLang > English.
func ResolveLocale(channelLocale, msgLang string) language.Tag {
	candidates := make([]language.Tag, 0, 2)
	if channelLocale != "" {
		tag, err := language.Parse(channelLocale)
		if err == nil {
			candidates = append(candidates, tag)
		}
	}
	if msgLang != "" {
		tag, err := language.Parse(msgLang)
		if err == nil {
			candidates = append(candidates, tag)
		}
	}
	if len(candidates) == 0 {
		return language.English
	}
	_, idx, _ := matcher.Match(candidates...)
	return SupportedLanguages[idx]
}

// LanguageName returns the human-readable English name for a locale tag.
// Used in LLM directives like "Respond in Russian."
func LanguageName(tag language.Tag) string {
	base, _ := tag.Base()
	switch base.String() {
	case "en":
		return "English"
	case "ru":
		return "Russian"
	case "zh":
		return "Chinese"
	case "es":
		return "Spanish"
	case "fr":
		return "French"
	case "he":
		return "Hebrew"
	default:
		return "English"
	}
}

// TagString returns the short BCP-47 base tag string (e.g. "en", "ru").
func TagString(tag language.Tag) string {
	base, _ := tag.Base()
	return base.String()
}

// IsSupported checks if a locale string maps to a supported language.
func IsSupported(locale string) bool {
	if locale == "" {
		return false
	}
	tag, err := language.Parse(locale)
	if err != nil {
		return false
	}
	_, _, conf := matcher.Match(tag)
	return conf >= language.High
}

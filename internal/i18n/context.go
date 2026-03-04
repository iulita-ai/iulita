package i18n

import (
	"context"

	"golang.org/x/text/language"
)

type localeKey struct{}

// WithLocale returns a context enriched with the active locale tag.
func WithLocale(ctx context.Context, tag language.Tag) context.Context {
	return context.WithValue(ctx, localeKey{}, tag)
}

// LocaleFrom extracts the active locale from context.
// Returns language.English if not set.
func LocaleFrom(ctx context.Context) language.Tag {
	if v, ok := ctx.Value(localeKey{}).(language.Tag); ok {
		return v
	}
	return language.English
}

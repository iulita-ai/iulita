package telegram

import (
	_ "embed"
	"strings"
)

// Only trusted, embedded templates are composed here. Runtime data stays in
// Telegram state and is inserted as text, never as HTML or style content.
//
//go:embed widgets/card.css
var cardCSS string

//go:embed widgets/day.html
var dayMarkup string

//go:embed widgets/public.html
var publicMarkup string

//go:embed widgets/weather.html
var weatherMarkup string

//go:embed widgets/weather-art.svg
var weatherArt string

func cardDocument(markup string) string {
	styled := strings.Replace(markup, "<!--CARD_STYLE-->", "<style>"+cardCSS+"</style>", 1)
	return strings.ReplaceAll(styled, "<!--WEATHER_ART-->", weatherArt)
}

var dayHTML = cardDocument(dayMarkup)
var publicWidgetHTML = cardDocument(publicMarkup)
var weatherHTML = cardDocument(weatherMarkup)

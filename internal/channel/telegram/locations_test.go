package telegram

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/text/language"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/i18n"
)

func TestLocationFromMessage(t *testing.T) {
	if err := i18n.Init(); err != nil {
		t.Fatalf("i18n.Init() failed: %v", err)
	}
	tests := []struct {
		name    string
		msg     *tgbotapi.Message
		wantNil bool
		check   func(*channel.LocationAttachment) bool
	}{
		{
			name:    "nil message",
			msg:     nil,
			wantNil: true,
		},
		{
			name:    "neither location nor venue",
			msg:     &tgbotapi.Message{Text: "hello"},
			wantNil: true,
		},
		{
			name: "bare pin with accuracy",
			msg: &tgbotapi.Message{Location: &tgbotapi.Location{
				Latitude: 52.51627, Longitude: 13.37775, HorizontalAccuracy: 35,
			}},
			check: func(l *channel.LocationAttachment) bool {
				return l.Title == "" && l.Address == "" && !l.Live && l.Accuracy == 35 &&
					l.Latitude == 52.51627 && l.Longitude == 13.37775
			},
		},
		{
			name: "live pin",
			msg: &tgbotapi.Message{Location: &tgbotapi.Location{
				Latitude: 52.51627, Longitude: 13.37775, LivePeriod: 3600,
			}},
			check: func(l *channel.LocationAttachment) bool { return l.Live },
		},
		{
			name: "venue (subsumes location)",
			msg: &tgbotapi.Message{
				Venue: &tgbotapi.Venue{
					Location: tgbotapi.Location{Latitude: 52.51627, Longitude: 13.37775},
					Title:    "Brandenburg Gate",
					Address:  "Pariser Platz 1",
				},
				// Telegram also sets Location when Venue is set; venue must win.
				Location: &tgbotapi.Location{Latitude: 52.51627, Longitude: 13.37775},
			},
			check: func(l *channel.LocationAttachment) bool {
				return l.Title == "Brandenburg Gate" && l.Address == "Pariser Platz 1" && !l.Live
			},
		},
		{
			name: "venue with long title is rune-truncated",
			msg: &tgbotapi.Message{
				Venue: &tgbotapi.Venue{
					Location: tgbotapi.Location{Latitude: 1, Longitude: 2},
					Title:    strings.Repeat("é", 300),
					Address:  "x",
				},
			},
			check: func(l *channel.LocationAttachment) bool {
				return len([]rune(l.Title)) == 256
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := locationFromMessage(tt.msg)
			if tt.wantNil {
				if got != nil {
					t.Fatalf("expected nil, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected attachment, got nil")
			}
			if tt.check != nil && !tt.check(got) {
				t.Fatalf("attachment check failed: %+v", got)
			}
		})
	}
}

func TestFormatLocationText(t *testing.T) {
	if err := i18n.Init(); err != nil {
		t.Fatalf("i18n.Init() failed: %v", err)
	}
	tests := []struct {
		name string
		tag  language.Tag
		loc  *channel.LocationAttachment
		want string
	}{
		{
			name: "bare pin en",
			tag:  language.English,
			loc:  &channel.LocationAttachment{Latitude: 52.51627, Longitude: 13.37775},
			want: "[Location]: 52.516270, 13.377750",
		},
		{
			name: "pin with accuracy",
			tag:  language.English,
			loc:  &channel.LocationAttachment{Latitude: 52.51627, Longitude: 13.37775, Accuracy: 35},
			want: "[Location]: 52.516270, 13.377750 (±35 m)",
		},
		{
			name: "live pin",
			tag:  language.English,
			loc:  &channel.LocationAttachment{Latitude: 52.51627, Longitude: 13.37775, Live: true},
			want: "[Location]: 52.516270, 13.377750 (live location — updates not tracked)",
		},
		{
			name: "venue",
			tag:  language.English,
			loc: &channel.LocationAttachment{
				Latitude: 52.51627, Longitude: 13.37775,
				Title: "Brandenburg Gate", Address: "Pariser Platz 1",
			},
			want: "[Place]: Brandenburg Gate — Pariser Platz 1 (52.516270, 13.377750)",
		},
		{
			name: "venue without address",
			tag:  language.English,
			loc: &channel.LocationAttachment{
				Latitude: 52.51627, Longitude: 13.37775,
				Title: "Brandenburg Gate",
			},
			want: "[Place]: Brandenburg Gate (52.516270, 13.377750)",
		},
		{
			name: "ru accuracy suffix uses cyrillic m",
			tag:  language.Russian,
			loc:  &channel.LocationAttachment{Latitude: 1.5, Longitude: 2.5, Accuracy: 10},
			want: "[Location]: 1.500000, 2.500000 (±10 м)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatLocationText(tt.tag, tt.loc)
			if got != tt.want {
				t.Fatalf("formatLocationText() = %q, want %q", got, tt.want)
			}
		})
	}

	t.Run("hebrew coords wrapped in LRM", func(t *testing.T) {
		loc := &channel.LocationAttachment{Latitude: 52.51627, Longitude: 13.37775}
		got := formatLocationText(language.Hebrew, loc)
		if !strings.Contains(got, "\u200e52.516270, 13.377750\u200e") {
			t.Fatalf("hebrew coords not LRM-wrapped: %q", got)
		}
	})
}

func TestFormatCoordsNegative(t *testing.T) {
	got := channel.FormatCoords(-33.865143, -70.995777)
	want := "-33.865143, -70.995777"
	if got != want {
		t.Fatalf("FormatCoords() = %q, want %q", got, want)
	}
}

func TestHasUpdateContent(t *testing.T) {
	tests := []struct {
		name string
		msg  *tgbotapi.Message
		want bool
	}{
		{"nil", nil, false},
		{"text", &tgbotapi.Message{Text: "hi"}, true},
		{"photo", &tgbotapi.Message{Photo: []tgbotapi.PhotoSize{{FileID: "x"}}}, true},
		{"document", &tgbotapi.Message{Document: &tgbotapi.Document{FileID: "x"}}, true},
		{"voice", &tgbotapi.Message{Voice: &tgbotapi.Voice{FileID: "x"}}, true},
		{"audio", &tgbotapi.Message{Audio: &tgbotapi.Audio{FileID: "x"}}, true},
		{"location", &tgbotapi.Message{Location: &tgbotapi.Location{Latitude: 1, Longitude: 2}}, true},
		{"venue", &tgbotapi.Message{Venue: &tgbotapi.Venue{Title: "t"}}, true},
		{"sticker", &tgbotapi.Message{Sticker: &tgbotapi.Sticker{FileID: "x"}}, false},
		{"empty", &tgbotapi.Message{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasUpdateContent(tt.msg); got != tt.want {
				t.Fatalf("hasUpdateContent(%s) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("héllo", 3); got != "hél" {
		t.Fatalf("truncateRunes multibyte = %q", got)
	}
	if got := truncateRunes("abc", 10); got != "abc" {
		t.Fatalf("truncateRunes under limit = %q", got)
	}
}

func TestNudgeZeroAxis(t *testing.T) {
	if got := nudgeZeroAxis(0); got != 1e-8 {
		t.Fatalf("nudgeZeroAxis(0) = %v", got)
	}
	if got := nudgeZeroAxis(-13.3777); got != -13.3777 {
		t.Fatalf("nudgeZeroAxis(non-zero) = %v", got)
	}
}

func TestRound6(t *testing.T) {
	tests := []struct {
		in, want float64
	}{
		{52.516270000000003, 52.51627},
		{13.37775, 13.37775},
		{-33.8651439, -33.865144},
		{1e-9, 0},
	}
	for _, tt := range tests {
		if got := round6(tt.in); got != tt.want {
			t.Fatalf("round6(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestVenueConfigTruncates(t *testing.T) {
	cfg := venueConfig(100, channel.OutboundVenue{
		Location: channel.OutboundLocation{Latitude: 52.516270000000003, Longitude: 0},
		Title:    strings.Repeat("T", 200),
		Address:  strings.Repeat("A", 300),
	})
	if got := len([]rune(cfg.Title)); got != 128 {
		t.Fatalf("title not truncated to 128 (Bot API limit): %d", got)
	}
	if got := len([]rune(cfg.Address)); got != 256 {
		t.Fatalf("address not truncated to 256: %d", got)
	}
	if cfg.Latitude != 52.51627 {
		t.Fatalf("latitude not rounded: %v", cfg.Latitude)
	}
	if cfg.Longitude != 1e-8 {
		t.Fatalf("zero longitude not nudged: %v", cfg.Longitude)
	}
}

// TestSanitizeTGError guards against leaking the bot token: tgbotapi returns
// raw *url.Error whose URL embeds api.telegram.org/bot<TOKEN>/….
func TestSanitizeTGError(t *testing.T) {
	inner := errors.New("dial tcp: lookup api.telegram.org: no such host")
	uerr := &url.Error{Op: "Post", URL: "https://api.telegram.org/bot123:SECRET/sendLocation", Err: inner}
	got := sanitizeTGError(uerr)
	if !errors.Is(got, inner) {
		t.Fatalf("url.Error not stripped: %v", got)
	}
	if strings.Contains(got.Error(), "SECRET") {
		t.Fatalf("token leaked through sanitizer: %v", got)
	}
	plain := errors.New("some other error")
	if got := sanitizeTGError(plain); !errors.Is(got, plain) {
		t.Fatalf("non-url error must pass through: %v", got)
	}
}

package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/defaults"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/i18n"
	"github.com/iulita-ai/iulita/internal/security"
	weatherSkill "github.com/iulita-ai/iulita/internal/skill/weather"
)

// WeatherController uses native provider data, never model-generated markup.
type WeatherController struct {
	Fetch   func(context.Context, string, int) (*domain.WeatherCard, error)
	Enabled func() bool
	Widgets bool
	limiter DayController
}

// SetWeatherController attaches the weather reader and widget settings.
func (c *Channel) SetWeatherController(w *WeatherController) { c.weather = w }

func weatherSensitive(text string) bool {
	found, _ := responseSensitivity(text)
	return found
}

func responseSensitivity(text string) (sensitive bool, classification string) {
	if found, kind := security.Contains(text); found {
		return true, kind
	}
	if security.RedactPaymentData(text) != text {
		return true, "payment_data"
	}
	return false, ""
}

func (c *Channel) weatherTarget(ctx context.Context, conversation, owner string) (target channel.TelegramTarget, available bool) {
	t, e := c.resolveTarget(ctx, conversation)
	// A zero pin is a legacy route, still usable for ordinary read-only replies.
	// Destructive lifecycle synchronization has a separate, strict identity gate.
	return t, e == nil && ctx.Err() == nil && t.Private && owner != "" && t.OwnerID == owner && t.ChatID > 0 && c.isAllowed(t.ChatID) && c.botID > 0 && (t.BotID == 0 || t.BotID == c.botID)
}

func (c *Channel) sendWeatherText(ctx context.Context, conversation, owner, text string, markdown ...bool) {
	if weatherSensitive(text) {
		return
	}
	chunks := channel.VerbatimChunks(text, 4096)
	for i, chunk := range chunks {
		t, ok := c.weatherTarget(ctx, conversation, owner)
		if !ok {
			return
		}
		params := &bot.SendMessageParams{ChatID: t.ChatID, MessageThreadID: t.ThreadID, Text: chunk, ProtectContent: true, LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: bot.True()}}
		formatted := len(markdown) > 0 && markdown[0]
		if formatted {
			params.Text = toTelegramHTML(chunk)
			params.ParseMode = models.ParseModeHTML
		}
		message, e := c.bot.SendMessage(ctx, params)
		if e != nil {
			if missingTelegramTopic(e) {
				if deliveryErr := c.observeMissingTopic(ctx, t.ChatID, t.ThreadID, e); deliveryErr != nil {
					c.noteFailure("Telegram observeMissingTopic failed", deliveryErr)
				}
			} else if formatted && errors.Is(e, bot.ErrorBadRequest) {
				// Retry rejected formatting literally, through the same per-send scope guard.
				c.sendWeatherText(ctx, conversation, owner, strings.Join(chunks[i:], ""))
			}
			return
		}
		if c.sentMsgs != nil {
			c.sentMsgs.record(t.ChatID, message.ID, chunk, t.ThreadID)
		}
	}
}

type weatherMetric struct {
	Label string `json:"l"`
	Value string `json:"v"`
}
type weatherDayState struct {
	Day         string `json:"d"`
	Temperature string `json:"t"`
	Condition   string `json:"k"`
}
type weatherState struct {
	Location    string            `json:"l"`
	Period      string            `json:"p"`
	Temperature string            `json:"t"`
	Condition   string            `json:"c"`
	Range       string            `json:"r"`
	Kind        string            `json:"k"`
	Night       bool              `json:"n,omitempty"`
	Forecast    bool              `json:"f,omitempty"`
	Metrics     []weatherMetric   `json:"m,omitempty"`
	Days        []weatherDayState `json:"d"`
	Source      string            `json:"s"`
	Updated     string            `json:"u"`
	Lang        string            `json:"a"`
}

func weatherTemperature(v *float64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%.0f°", *v)
}
func weatherRange(d domain.WeatherDay) string {
	return weatherTemperature(d.TempMinC) + " / " + weatherTemperature(d.TempMaxC)
}
func weatherConditionLabel(locale, condition string) string {
	labels := map[string][2]string{"clear": {"Clear", "Ясно"}, "partly_cloudy": {"Partly cloudy", "Переменная облачность"}, "cloudy": {"Cloudy", "Облачно"}, "rain": {"Rain", "Дождь"}, "snow": {"Snow", "Снег"}, "thunderstorm": {"Thunderstorm", "Гроза"}, "fog": {"Fog", "Туман"}, "unknown": {"Conditions unavailable", "Нет данных об условиях"}}
	v, ok := labels[condition]
	if !ok {
		v = labels["unknown"]
	}
	return dayText(locale, v[0], v[1])
}

func weatherProjection(card *domain.WeatherCard, locale string) ([]string, error) {
	if card == nil || len(card.Days) == 0 || len(card.Days) > 16 {
		return nil, fmt.Errorf("invalid forecast")
	}
	lang := "en"
	if strings.HasPrefix(locale, "ru") {
		lang = "ru"
	}
	first := card.Days[0]
	s := weatherState{Location: truncateRunes(security.RedactSensitiveText(card.Location), 60), Period: dayText(lang, "Forecast", "Прогноз"), Temperature: weatherRange(first), Range: dayText(lang, "Low / high", "Минимум / максимум"), Kind: string(first.Condition), Source: truncateRunes(card.Source, 30), Updated: card.RetrievedAt.UTC().Format("15:04 UTC"), Lang: lang}
	s.Forecast = true
	if current := card.Current; current != nil && current.TemperatureC != nil {
		s.Forecast = false
		s.Temperature = weatherTemperature(current.TemperatureC)
		s.Period = dayText(lang, "Now", "Сейчас")
		s.Kind = string(current.Condition)
		s.Range = weatherRange(first)
		s.Night = current.IsDay != nil && !*current.IsDay
		if current.FeelsLikeC != nil {
			s.Metrics = append(s.Metrics, weatherMetric{dayText(lang, "Feels like", "Ощущается"), weatherTemperature(current.FeelsLikeC)})
		}
		if current.WindKph != nil {
			s.Metrics = append(s.Metrics, weatherMetric{dayText(lang, "Wind", "Ветер"), fmt.Sprintf("%.0f %s", *current.WindKph, dayText(lang, "km/h", "км/ч"))})
		}
		if current.HumidityPercent != nil {
			s.Metrics = append(s.Metrics, weatherMetric{dayText(lang, "Humidity", "Влажность"), fmt.Sprintf("%d%%", *current.HumidityPercent)})
		}
	}
	s.Condition = weatherConditionLabel(lang, s.Kind)
	// Return every available day. Compact pages avoid Telegram's 1024-byte state limit.
	var pages []string
	for start := 0; start < len(card.Days); {
		end := start + 4
		if end > len(card.Days) {
			end = len(card.Days)
		}
		for {
			s.Days = nil
			for _, d := range card.Days[start:end] {
				label := d.Date
				if parsed, e := time.Parse("2006-01-02", d.Date); e == nil {
					label = parsed.Format("02.01")
				}
				s.Days = append(s.Days, weatherDayState{label, weatherRange(d), string(d.Condition)})
			}
			s.Period = dayText(lang, "Forecast", "Прогноз")
			if card.Current != nil && card.Current.TemperatureC != nil {
				s.Period = dayText(lang, "Now", "Сейчас")
			}
			if len(card.Days) > 4 {
				s.Period += fmt.Sprintf(" · %d–%d/%d", start+1, end, len(card.Days))
			}
			if card.RequestedDays > len(card.Days) {
				s.Period += dayText(lang, " · limited coverage", " · неполный прогноз")
			}
			raw, e := json.Marshal(s)
			if e != nil {
				return nil, e
			}
			if len(raw) <= 1024 {
				pages = append(pages, string(raw))
				break
			}
			if end-start > 1 {
				end--
				continue
			}
			return nil, fmt.Errorf("forecast state exceeds budget")
		}
		start = end
	}
	return pages, nil
}

// sendWeather returns true after it has handled the response, including safe text fallback.
func (c *Channel) sendWeather(ctx context.Context, conversation, owner, locale string, card *domain.WeatherCard, response string) bool {
	if c.weather == nil || !c.weather.Widgets || (c.weather.Enabled != nil && !c.weather.Enabled()) {
		return false
	}
	if weatherSensitive(response) {
		return false
	}
	fallback := weatherSkill.FormatCardText(i18n.WithLocale(ctx, i18n.ResolveLocale(locale, "en")), card)
	formatResponse := response != "" && response != fallback
	if weatherSensitive(fallback) {
		return false
	}
	pages, e := weatherProjection(card, locale)
	if e != nil || validateWidgetHTML(weatherHTML) != nil {
		if response != "" && response != fallback {
			fallback += "\n\n" + response
		}
		c.sendWeatherText(ctx, conversation, owner, fallback, formatResponse)
		return true
	}
	for _, state := range pages {
		if weatherSensitive(state) {
			return false
		}
	}
	failed := false
	for i, state := range pages {
		target, ok := c.weatherTarget(ctx, conversation, owner)
		if !ok {
			return true
		}
		chat, thread := target.ChatID, target.ThreadID
		caption := dayText(locale, "Weather · /weather text CITY", "Погода · /weather text ГОРОД")
		if len(pages) > 1 {
			caption += fmt.Sprintf(" · %d/%d", i+1, len(pages))
		}
		_, e := c.bot.SendWidget(ctx, &bot.SendWidgetParams{ChatID: chat, MessageThreadID: thread, HTML: weatherHTML, ChatData: state, Caption: caption, ProtectContent: true})
		if e != nil {
			if missingTelegramTopic(e) {
				if deliveryErr := c.observeMissingTopic(ctx, chat, thread, e); deliveryErr != nil {
					c.noteFailure("Telegram observeMissingTopic failed", deliveryErr)
				}
				return true
			}
			failed = true
			break
		}
	}
	// Keep the full synthesized answer and forecast in the ordinary conversation.
	if response == "" {
		response = fallback
	}
	if failed && response != fallback {
		response = fallback + "\n\n" + response
	}
	if weatherSensitive(response) {
		return true
	}
	c.sendWeatherText(ctx, conversation, owner, response, formatResponse)
	return true
}

func (c *Channel) handleWeatherCommand(ctx context.Context, m *models.Message) bool {
	fields := strings.Fields(m.Text)
	if len(fields) == 0 {
		return false
	}
	cmd, addressed := c.commandName(fields[0])
	if !addressed || cmd != "/weather" {
		return false
	}
	if m.From == nil || m.Chat.Type != models.ChatTypePrivate || m.From.ID != m.Chat.ID || !c.isAllowed(m.From.ID) {
		return true
	}
	locale := m.From.LanguageCode
	if c.store != nil {
		if saved, err := c.store.GetChannelLocale(ctx, "telegram", strconv.FormatInt(m.From.ID, 10)); err == nil {
			locale = i18n.ResolveLocale(saved, locale).String()
		}
	}
	if c.weather == nil || c.weather.Fetch == nil || (c.weather.Enabled != nil && !c.weather.Enabled()) {
		c.sendSingleMessage(m.Chat.ID, dayText(locale, "Weather is disabled.", "Погода отключена."), 0, m.MessageThreadID)
		return true
	}
	args := fields[1:]
	textMode := len(args) > 0 && args[0] == "text"
	if textMode {
		args = args[1:]
	}
	days := 4
	if len(args) > 1 {
		if n, e := strconv.Atoi(args[len(args)-1]); e == nil && n >= 1 && n <= 16 {
			days = n
			args = args[:len(args)-1]
		}
	}
	if len(args) == 0 {
		args = []string{defaults.City}
	}
	owner, e := c.dayOwner(ctx, *m.From, m.Chat.ID, m.MessageThreadID)
	if e != nil {
		return true
	}
	if !c.weather.limiter.admit(owner) {
		return true
	}
	conversation, e := c.conversationFor(ctx, m)
	if e != nil {
		return true
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		work, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		card, e := c.weather.Fetch(work, strings.Join(args, " "), days)
		if _, routeErr := c.resolveTarget(work, conversation); routeErr != nil {
			return
		}
		if e != nil {
			c.sendWeatherText(work, conversation, owner, dayText(locale, "Could not load weather. Please try later.", "Не удалось загрузить погоду. Повторите позже."))
			return
		}
		text := weatherSkill.FormatCardText(i18n.WithLocale(work, i18n.ResolveLocale(locale, "en")), card)
		if !textMode && c.sendWeather(work, conversation, owner, locale, card, text) {
			return
		}
		c.sendWeatherText(work, conversation, owner, text)
	}()
	return true
}

package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/iulita-ai/iulita/internal/dayview"
	"github.com/iulita-ai/iulita/internal/security"
	"github.com/iulita-ai/iulita/internal/skill/interact"
)

type publicSnapshotStore interface {
	SavePublicSnapshot(context.Context, *dayview.PublicSnapshot) error
	GetPublicSnapshot(context.Context, string, string) (*dayview.PublicSnapshot, error)
}

// SetInlineWidgetTestMode permits inline widgets only on a verified test environment.
func (c *Channel) SetInlineWidgetTestMode(enabled bool) { c.inlineTest = enabled && c.testEnvironment }
func (c *Channel) inlineEnabled() bool                  { return c.inlineTest && c.testEnvironment }

func publicWidgetState(p *dayview.PublicSnapshot) string { return publicWidgetPage(p, 0) }
func publicWidgetPage(p *dayview.PublicSnapshot, page int) string {
	text := []rune(p.Text)
	state := map[string]string{}
	if p.Withdrawn || !time.Now().Before(p.ExpiresAt) {
		state["t"] = "This public result was withdrawn. Existing copies may remain."
		state["w"] = "1"
	} else {
		pages := (len(text) + 99) / 100
		if pages < 1 {
			pages = 1
		}
		if page < 0 || page >= pages {
			page = 0
		}
		start := page * 100
		end := start + 100
		if end > len(text) {
			end = len(text)
		}
		state["t"] = string(text[start:end])
		state["q"] = fmt.Sprintf("pub:%s:%d", p.ID, page)
		if page > 0 {
			state["p"] = fmt.Sprintf("pub:%s:%d", p.ID, page-1)
		}
		if page+1 < pages {
			state["n"] = fmt.Sprintf("pub:%s:%d", p.ID, page+1)
		}
	}
	raw, marshalErr := json.Marshal(state)
	if marshalErr != nil {
		return `{"w":"1"}`
	}
	return string(raw)
}

// inlineCalculator is deterministic and never reads caller history or tools.
func inlineCalculator(query string) (string, error) {
	if len(query) > 180 {
		return "", fmt.Errorf("query too long")
	}
	parts := strings.Fields(query)
	if len(parts) == 3 && parts[0] == "time" {
		at, e := time.Parse(time.RFC3339, parts[1])
		if e != nil {
			return "", fmt.Errorf("use an RFC3339 timestamp")
		}
		loc, e := time.LoadLocation(parts[2])
		if e != nil {
			return "", fmt.Errorf("invalid IANA timezone")
		}
		return at.In(loc).Format("2006-01-02 15:04 MST"), nil
	}
	if !strings.HasPrefix(query, "calc ") {
		return "", fmt.Errorf("examples: calc 12 * 3; time 2026-12-01T12:00:00+03:00 Europe/Berlin")
	}
	expression := strings.TrimSpace(strings.TrimPrefix(query, "calc "))
	node, parseErr := parser.ParseExpr(expression)
	if parseErr != nil {
		return "", fmt.Errorf("invalid calculation")
	}
	var eval func(ast.Expr, int) (float64, error)
	eval = func(n ast.Expr, depth int) (float64, error) {
		if depth > 16 {
			return 0, fmt.Errorf("calculation too complex")
		}
		switch v := n.(type) {
		case *ast.BasicLit:
			if v.Kind != token.INT && v.Kind != token.FLOAT {
				return 0, fmt.Errorf("numbers only")
			}
			return strconv.ParseFloat(v.Value, 64)
		case *ast.ParenExpr:
			return eval(v.X, depth+1)
		case *ast.UnaryExpr:
			x, e := eval(v.X, depth+1)
			if e != nil {
				return 0, e
			}
			if v.Op == token.SUB {
				return -x, nil
			}
			if v.Op == token.ADD {
				return x, nil
			}
		case *ast.BinaryExpr:
			x, e := eval(v.X, depth+1)
			if e != nil {
				return 0, e
			}
			y, e := eval(v.Y, depth+1)
			if e != nil {
				return 0, e
			}
			switch v.Op {
			case token.ADD:
				return x + y, nil
			case token.SUB:
				return x - y, nil
			case token.MUL:
				return x * y, nil
			case token.QUO:
				if y != 0 {
					return x / y, nil
				}
			}
		}
		return 0, fmt.Errorf("unsupported calculation")
	}
	answer, e := eval(node, 0)
	if e != nil || math.IsNaN(answer) || math.IsInf(answer, 0) {
		return "", fmt.Errorf("invalid calculation")
	}
	return expression + " = " + strconv.FormatFloat(answer, 'g', 10, 64), nil
}
func (c *Channel) answerPublicInline(ctx context.Context, q *models.InlineQuery) {
	var results []models.InlineQueryResult
	if strings.HasPrefix(q.Query, "share ") && c.day != nil {
		if store, ok := c.day.Store.(publicSnapshotStore); ok {
			p, e := store.GetPublicSnapshot(ctx, c.instanceID, strings.TrimSpace(strings.TrimPrefix(q.Query, "share ")))
			if e == nil && p != nil && !p.Withdrawn && time.Now().Before(p.ExpiresAt) && p.WidgetID != "" {
				results = append(results, &models.InlineQueryResultArticle{ID: p.ID, Title: "Public result", Description: truncateRunes(p.Text, 60), InputMessageContent: models.InputSharedWidgetMessageContent{WidgetID: p.WidgetID, Caption: "Public snapshot · " + p.CreatedAt.UTC().Format(time.RFC3339)}})
			}
		}
	} else {
		answer, e := inlineCalculator(q.Query)
		title := "Calculator / timezone"
		if e != nil {
			answer = e.Error()
			title = "Try: calc 12 * 3"
		}
		raw, marshalErr := json.Marshal(map[string]string{"t": answer})
		if marshalErr != nil {
			return
		}
		results = append(results, &models.InlineQueryResultArticle{ID: dayview.Revision(answer), Title: title, Description: truncateRunes(answer, 100), InputMessageContent: models.InputWidgetMessageContent{HTML: publicWidgetHTML, PublicData: string(raw), Caption: answer}})
	}
	if _, deliveryErr := c.bot.AnswerInlineQuery(ctx, &bot.AnswerInlineQueryParams{InlineQueryID: q.ID, Results: results, CacheTime: 0, IsPersonal: true}); deliveryErr != nil {
		c.noteFailure("Telegram AnswerInlineQuery failed", deliveryErr)
	}
}
func (c *Channel) publishSnapshot(ctx context.Context, card *dayview.Card, data cardData, text string) {
	if _, routeErr := c.resolveTarget(ctx, data.Conversation); routeErr != nil {
		return
	}
	if validateWidgetHTML(publicWidgetHTML) != nil {
		return
	}
	if !c.inlineEnabled() {
		c.sendSingleMessage(card.Chat, dayText(data.Locale, "Public widgets are available only on Telegram's test server.", "Публичные виджеты доступны только на тестовом сервере Telegram."), 0, card.Thread)
		return
	}
	store, ok := c.day.Store.(publicSnapshotStore)
	if !ok {
		return
	}
	text = truncateRunes(security.RedactSensitiveText(text), 1500)
	if strings.TrimSpace(text) == "" {
		return
	}
	nonce := generateNonce()
	id := generateNonce()
	conf := &telegramConfirmer{channel: c, chatID: card.Chat, threadID: card.Thread, fromTg: card.Chat}
	preview := dayText(data.Locale, "Publish this exact public snapshot? Recipients can keep copies.\n\n", "Опубликовать этот точный снимок? Получатели могут сохранить копии.\n\n") + text
	decision, confirmErr := conf.Confirm(ctx, interact.Confirmation{Nonce: nonce, OperationID: dayview.Revision(card.Owner, id, text), Reason: interact.ConfirmMCP, Preview: preview, IssuedAt: time.Now(), ExpiresAt: time.Now().Add(90 * time.Second)}, interact.Actor{UserID: card.Owner, Channel: "telegram", ChannelInstance: c.instanceID, ExternalID: strconv.FormatInt(card.Chat, 10)})
	if confirmErr != nil || !decision.Approved {
		return
	}
	if _, routeErr := c.resolveTarget(ctx, data.Conversation); routeErr != nil {
		return
	}
	current, e := c.day.Store.GetDayCard(ctx, card.ID)
	if e != nil || current == nil || current.Owner != card.Owner || current.Revision != card.Revision || current.Instance != c.instanceID || !time.Now().Before(current.Expires) {
		return
	}
	p := &dayview.PublicSnapshot{ID: id, Owner: card.Owner, Instance: c.instanceID, Text: text, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(7 * 24 * time.Hour)}
	if e = store.SavePublicSnapshot(ctx, p); e != nil {
		return
	}
	msg, e := c.bot.SendWidget(ctx, &bot.SendWidgetParams{ChatID: card.Chat, MessageThreadID: card.Thread, HTML: publicWidgetHTML, PublicData: publicWidgetState(p), IsShared: true, AllowQueries: true, Caption: "Public snapshot · " + p.CreatedAt.UTC().Format(time.RFC3339)})
	if e != nil || msg.Widget == nil || msg.Widget.ID == "" {
		c.sendSingleMessage(card.Chat, dayText(data.Locale, "Publication could not be confirmed. Do not repeat automatically.", "Не удалось подтвердить публикацию. Не повторяйте её автоматически."), 0, card.Thread)
		return
	}
	p.WidgetID = msg.Widget.ID
	if store.SavePublicSnapshot(ctx, p) != nil {
		return
	}
	c.sendSingleMessage(card.Chat, dayText(data.Locale, "Published. Share with inline query: share ", "Опубликовано. Inline-запрос для отправки: share ")+p.ID+"\n/unpublish "+p.ID, 0, card.Thread)
}
func (c *Channel) publishDayResult(ctx context.Context, card *dayview.Card, data cardData, index int) {
	var items []dayview.Item
	for _, s := range data.Sections {
		items = append(items, s.Items...)
	}
	if index < 0 || index >= len(items) {
		return
	}
	detail, e := c.day.Service(data.Conversation).Detail(ctx, card.Owner, items[index])
	if e != nil {
		return
	}
	text := detail.Result
	if text == "" {
		text = detail.Title + "\n" + detail.Summary
	}
	c.publishSnapshot(ctx, card, data, text)
}

func (c *Channel) handlePublicCommand(ctx context.Context, m *models.Message, cmd string) bool {
	if cmd != "/publish" && cmd != "/unpublish" {
		return false
	}
	if m.From == nil || m.Chat.Type != models.ChatTypePrivate || c.day == nil {
		return true
	}
	if !c.inlineEnabled() {
		c.sendSingleMessage(m.Chat.ID, dayText(m.From.LanguageCode, "Public exports run only on Telegram's test server.", "Публичная отправка доступна только на тестовом сервере Telegram."), 0, m.MessageThreadID)
		return true
	}
	owner, e := c.dayOwner(ctx, *m.From, m.Chat.ID, m.MessageThreadID)
	if e != nil {
		return true
	}
	if !c.day.admit(owner) {
		return true
	}
	store, ok := c.day.Store.(publicSnapshotStore)
	if !ok {
		return true
	}
	if cmd == "/unpublish" {
		fields := strings.Fields(m.Text)
		if len(fields) != 2 {
			return true
		}
		p, snapshotErr := store.GetPublicSnapshot(ctx, c.instanceID, fields[1])
		if snapshotErr != nil || p == nil || p.Owner != owner {
			return true
		}
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			call, cancel := context.WithTimeout(ctx, 100*time.Second)
			defer cancel()
			nonce := generateNonce()
			conf := &telegramConfirmer{channel: c, chatID: m.Chat.ID, threadID: m.MessageThreadID, fromTg: m.From.ID}
			decision, confirmErr := conf.Confirm(call, interact.Confirmation{Nonce: nonce, OperationID: dayview.Revision(p.ID, "withdraw"), Reason: interact.ConfirmMCP, Preview: dayText(m.From.LanguageCode, "Withdraw the public snapshot? Existing copies may remain.", "Отозвать публичный снимок? Существующие копии могут сохраниться."), IssuedAt: time.Now(), ExpiresAt: time.Now().Add(90 * time.Second)}, interact.Actor{UserID: owner, Channel: "telegram", ExternalID: strconv.FormatInt(m.From.ID, 10), ChannelInstance: c.instanceID})
			if confirmErr != nil || !decision.Approved {
				return
			}
			p.Withdrawn = true
			if store.SavePublicSnapshot(call, p) != nil {
				return
			}
			state := publicWidgetState(p)
			if p.WidgetID != "" {
				if _, deliveryErr := c.bot.EditSharedWidgetData(call, &bot.EditSharedWidgetDataParams{WidgetID: p.WidgetID, PublicData: state}); deliveryErr != nil {
					c.noteFailure("Telegram EditSharedWidgetData failed", deliveryErr)
				}
			}
		}()
		return true
	}
	source := m.ReplyToMessage
	if source == nil || source.Chat.ID != m.Chat.ID || source.MessageThreadID != m.MessageThreadID {
		c.sendSingleMessage(m.Chat.ID, dayText(m.From.LanguageCode, "Reply to a result with /publish to preview a public snapshot.", "Ответьте на результат командой /publish для предпросмотра публичного снимка."), 0, m.MessageThreadID)
		return true
	}
	text, _ := normalizedTelegramContent(source)
	if m.Quote != nil {
		_, q := normalizedTelegramContent(m)
		if q != nil && q.Verified {
			text = q.Text
		} else {
			return true
		}
	}
	key, e := c.conversationFor(ctx, m)
	if e != nil {
		return true
	}
	data := cardData{Locale: m.From.LanguageCode, Conversation: key, Zone: "UTC"}
	raw, marshalErr := json.Marshal(data)
	if marshalErr != nil {
		return true
	}
	card := &dayview.Card{ID: generateNonce(), Owner: owner, Instance: c.instanceID, Chat: m.Chat.ID, Thread: m.MessageThreadID, Revision: 1, Expires: time.Now().Add(15 * time.Minute), ItemsJSON: string(raw)}
	if c.day.Store.SaveDayCard(ctx, card) != nil {
		return true
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		call, cancel := context.WithTimeout(ctx, 100*time.Second)
		defer cancel()
		c.publishSnapshot(call, card, data, text)
	}()
	return true
}

func (c *Channel) answerPublicShared(ctx context.Context, q *models.SharedWidgetQuery) {
	answer := `{"t":"Public result unavailable"}`
	parts := strings.Split(q.Data, ":")
	if q.Type.Type == models.WidgetQueryTypeTypeData && len(parts) >= 2 && len(parts) <= 3 && parts[0] == "pub" && len(q.Data) <= 64 && c.day != nil {
		page := 0
		valid := true
		if len(parts) == 3 {
			var e error
			page, e = strconv.Atoi(parts[2])
			valid = e == nil && page >= 0
		}
		if store, ok := c.day.Store.(publicSnapshotStore); ok && valid {
			p, e := store.GetPublicSnapshot(ctx, c.instanceID, parts[1])
			if e == nil && p != nil && p.WidgetID == q.WidgetID {
				answer = publicWidgetPage(p, page)
			}
		}
	}
	if _, deliveryErr := c.bot.AnswerWidgetQuery(ctx, &bot.AnswerWidgetQueryParams{WidgetQueryID: q.ID, Data: answer}); deliveryErr != nil {
		c.noteFailure("Telegram AnswerWidgetQuery failed", deliveryErr)
	}
}

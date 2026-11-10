package telegram

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/iulita-ai/iulita/internal/dayview"
	"github.com/iulita-ai/iulita/internal/ratelimit"
	"github.com/iulita-ai/iulita/internal/security"
	"github.com/iulita-ai/iulita/internal/skill/interact"
)

// DayController is explicitly attached at composition time. No global provider credentials
// are used without the provider's own verified owner boundary.
type DayController struct {
	once    sync.Once
	limiter *ratelimit.Limiter
	Actions bool
	Store   dayview.Store
	Service func(string) *dayview.Service // conversation-scoped sources
	mu      sync.Mutex
}

type dayRow struct {
	Title    string `json:"t"`
	Source   string `json:"o,omitempty"`
	Due      string `json:"d,omitempty"`
	Complete string `json:"c,omitempty"`
	Snooze   string `json:"s,omitempty"`
	Remind   string `json:"a,omitempty"`
	Details  string `json:"v,omitempty"`
	Download string `json:"z,omitempty"`
	Publish  string `json:"u,omitempty"`
	Resolve  string `json:"k,omitempty"`
}
type dayState struct {
	Header   string   `json:"h"`
	Rows     []dayRow `json:"r"`
	Previous string   `json:"p,omitempty"`
	Next     string   `json:"n,omitempty"`
	Refresh  string   `json:"f"`
	Status   string   `json:"m"`
	Lang     string   `json:"l"`
	Category string   `json:"g"`
	Switch   string   `json:"x"`
}
type cardData struct {
	Sections        []dayview.Section
	Conversation    string
	Zone            string
	Page            int
	Section         int
	Locale          string
	Actions         bool
	OperationStatus string
	TextMode        bool
	PublicTest      bool
}

func dayText(locale, en, ru string) string {
	if strings.HasPrefix(locale, "ru") {
		return ru
	}
	return en
}

func validateDayHTML() error { return validateWidgetHTML(dayHTML) }
func validateWidgetHTML(template string) error {
	var b bytes.Buffer
	w, err := zlib.NewWriterLevel(&b, zlib.BestCompression)
	if err != nil {
		return err
	}
	if _, err = w.Write([]byte(template)); err != nil {
		return err
	}
	if closeErr := w.Close(); closeErr != nil {
		return closeErr
	}
	if len(template) > 32768 || b.Len() > 4096 {
		return fmt.Errorf("widget template too large")
	}
	return nil
}
func dayAction(c *dayview.Card, verb string, index int) string {
	if verb == "acknowledge" {
		verb = "ack"
	}
	if verb == "reschedule" {
		verb = "move"
	}
	return fmt.Sprintf("d:%s:%s:%s:%d", c.ID, strconv.FormatInt(c.Revision, 36), verb, index)
}
func dayProjection(c *dayview.Card, data cardData) (string, error) {
	var rows []dayRow
	index := 0
	for _, section := range data.Sections {
		for itemIdx := range section.Items {
			i := &section.Items[itemIdx]
			itemIndex := index
			index++
			if daySection(section.Name) != data.Section {
				continue
			}
			r := dayRow{Title: truncateRunes(security.RedactSensitiveText(i.Title), 45), Source: truncateRunes(security.RedactSensitiveText(section.Name), 16), Due: truncateRunes(security.RedactSensitiveText(dayDueSummary(*i, data.Locale)), 25)}
			if matched, _ := security.Contains(r.Title); matched {
				r.Title = "[redacted]"
			}
			if data.Actions && data.OperationStatus != "unknown_outcome" && i.Complete {
				r.Complete = dayAction(c, "complete", itemIndex)
			}
			if data.Actions && data.OperationStatus != "unknown_outcome" && i.Reschedule {
				r.Snooze = dayAction(c, "reschedule", itemIndex)
			}
			if data.Actions && data.OperationStatus != "unknown_outcome" && (i.Source == "todoist" || i.Source == "google_tasks" || i.Source == "craft_tasks" || (i.Source == "reminders" && i.ResolveDelivery)) {
				r.Remind = dayAction(c, "remind", itemIndex)
			}
			if data.Actions && i.ResolveDelivery {
				r.Resolve = dayAction(c, "acknowledge", itemIndex)
			}
			r.Details = dayAction(c, "details", itemIndex)
			if i.Source == "jobs" {
				r.Download = dayAction(c, "result", itemIndex)
				if data.PublicTest {
					r.Publish = dayAction(c, "publish", itemIndex)
				}
			}
			rows = append(rows, r)
		}
	}
	lang := "en"
	if strings.HasPrefix(data.Locale, "ru") {
		lang = "ru"
	}
	loc, locationErr := time.LoadLocation(data.Zone)
	if locationErr != nil {
		loc = time.UTC
	}
	st := dayState{Lang: lang, Header: dayText(lang, "My day", "Мой день") + " · " + time.Now().In(loc).Format("02.01") + " · " + truncateRunes(data.Zone, 40), Rows: []dayRow{}, Refresh: dayAction(c, "refresh", 0), Status: dayText(lang, "Updated ", "Обновлено ") + time.Now().In(loc).Format("15:04")}
	st.Category = dayCategoryLabel(lang, data.Section)
	st.Switch = dayAction(c, "section", (data.Section+1)%4)
	for _, s := range data.Sections {
		if daySection(s.Name) != data.Section {
			continue
		}
		if s.Status != "ready" {
			label := dayText(lang, "unavailable", "недоступен")
			if s.Status == "partial" {
				label = dayText(lang, "partially loaded", "загружено частично")
			}
			if s.Status == "not_connected" {
				label = dayText(lang, "not connected", "не подключён")
			}
			st.Status += " · " + truncateRunes(s.Name, 20) + ": " + label
		}
	}
	// One row per page keeps action handles, Unicode text and status below Telegram's byte limit.
	page := data.Page
	if page < 0 {
		page = 0
	}
	if len(rows) > 0 {
		if page >= len(rows) {
			page = len(rows) - 1
		}
		st.Rows = rows[page : page+1]
		st.Status += fmt.Sprintf(" · %d/%d", page+1, len(rows))
		if page > 0 {
			st.Previous = dayAction(c, "page", page-1)
		}
		if page+1 < len(rows) {
			st.Next = dayAction(c, "page", page+1)
		}
	} else {
		ready := false
		for _, section := range data.Sections {
			if daySection(section.Name) == data.Section && section.Status == "ready" {
				ready = true
			}
		}
		if ready {
			st.Status += dayText(lang, " · No items for today", " · На сегодня записей нет")
		} else {
			st.Status += dayText(lang, " · Connect a source or refresh", " · Подключите источник или обновите")
		}
	}
	if data.OperationStatus == "unknown_outcome" {
		st.Status = dayText(lang, "Checking result. Do not repeat the action.", "Проверяем результат. Не повторяйте действие.")
	}
	st.Status = truncateRunes(st.Status, 120)
	raw, e := json.Marshal(st)
	if e != nil {
		return "", e
	}
	if len(raw) > 1024 {
		st.Status = truncateRunes(st.Status, 65)
		if len(st.Rows) > 0 {
			st.Rows[0].Title = truncateRunes(st.Rows[0].Title, 32)
			st.Rows[0].Due = truncateRunes(st.Rows[0].Due, 20)
		}
		raw, e = json.Marshal(st)
		if e != nil {
			return "", e
		}
	}
	if len(raw) > 1024 {
		return "", fmt.Errorf("widget state exceeds budget")
	}
	return string(raw), nil
}
func (c *Channel) dayOwner(ctx context.Context, u models.User, chat int64, thread ...int) (string, error) {
	if !c.isAllowed(u.ID) || chat != u.ID || c.userResolver == nil {
		return "", fmt.Errorf("private owner required")
	}
	key, err := c.conversationFor(ctx, &models.Message{Chat: models.Chat{ID: chat, Type: models.ChatTypePrivate}, From: &u, MessageThreadID: firstThread(thread)})
	if err != nil {
		return "", err
	}
	if _, e := c.resolveTarget(ctx, key); e != nil {
		return "", fmt.Errorf("topic is paused or unavailable")
	}
	return c.userResolver.ResolveUser(ctx, "telegram", strconv.FormatInt(u.ID, 10), u.Username, key)
}
func (c *Channel) handleDayCommand(ctx context.Context, m *models.Message) bool {
	if len(strings.Fields(m.Text)) == 0 {
		return false
	}
	cmd, addressed := c.commandName(strings.Fields(m.Text)[0])
	if addressed && c.handlePublicCommand(ctx, m, cmd) {
		return true
	}
	if !addressed || cmd != "/day" {
		return false
	}
	if c.day == nil {
		locale := "en"
		if m.From != nil {
			locale = m.From.LanguageCode
		}
		c.sendSingleMessage(m.Chat.ID, dayText(locale, "Day cards are not enabled for this bot yet.", "Карточки дня пока не включены для этого бота."), 0, m.MessageThreadID)
		return true
	}
	if m.From == nil {
		return true
	}
	if m.Chat.Type != models.ChatTypePrivate {
		if _, sendErr := c.bot.SendMessage(ctx, &bot.SendMessageParams{ChatID: m.Chat.ID, MessageThreadID: m.MessageThreadID, Text: dayText(m.From.LanguageCode, "Open your personal day with the button. Only you will see the reply. For interactive cards, open the bot privately and send /day text.", "Откройте личный день кнопкой: ответ увидите только вы. Для действий откройте бота в личке и отправьте /day text."), ReplyMarkup: models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: dayText(m.From.LanguageCode, "My day — private reply", "Мой день — личный ответ"), CallbackData: "myday:personal"}}}}}); sendErr != nil {
			c.noteFailure("Telegram personal day entry send failed", sendErr)
		}
		return true
	}
	owner, e := c.dayOwner(ctx, *m.From, m.Chat.ID, m.MessageThreadID)
	if e != nil {
		c.sendSingleMessage(m.Chat.ID, dayText(m.From.LanguageCode, "Could not verify your account.", "Не удалось проверить вашу учётную запись."), 0, m.MessageThreadID)
		return true
	}
	if !c.day.admit(owner) {
		c.sendSingleMessage(m.Chat.ID, dayText(m.From.LanguageCode, "Please wait before opening another card.", "Подождите перед открытием новой карточки."), 0, m.MessageThreadID)
		return true
	}
	c.wg.Add(1)
	go func() { defer c.wg.Done(); c.openDay(ctx, m, owner) }()
	return true
}
func (c *Channel) openDay(root context.Context, m *models.Message, owner string) {
	delivered := false
	defer func() {
		if !delivered {
			c.sendSingleMessage(m.Chat.ID, dayText(m.From.LanguageCode, "Could not load your day. Try /day text or retry later.", "Не удалось загрузить день. Попробуйте /day text или повторите позже."), 0, m.MessageThreadID)
		}
	}()
	ctx, cancel := context.WithTimeout(root, 14*time.Second)
	defer cancel()
	d := c.day
	zone := "UTC"
	if c.store != nil {
		if u, e := c.store.GetUser(ctx, owner); e == nil && u != nil && u.Timezone != "" {
			zone = u.Timezone
		}
	}
	loc, e := time.LoadLocation(zone)
	if e != nil {
		zone = "UTC"
		loc = time.UTC
	}
	conversation, e := c.conversationFor(ctx, m)
	if e != nil || c.persistRoute(ctx, conversation, m, owner) != nil {
		return
	}
	if _, routeErr := c.resolveTarget(ctx, conversation); routeErr != nil {
		return
	}
	card := &dayview.Card{ID: generateNonce(), Owner: owner, Instance: c.instanceID, Chat: m.Chat.ID, Thread: m.MessageThreadID, Revision: 1, Expires: time.Now().Add(24 * time.Hour)}
	data := cardData{Actions: d.Actions, Locale: m.From.LanguageCode, Conversation: conversation, Zone: zone, Sections: d.Service(conversation).Read(ctx, owner, time.Now(), loc)}
	sanitizeDaySections(data.Sections)
	if recovery, ok := d.Store.(dayview.RecoveryStore); ok {
		ops, recoveryErr := dayview.RecoverOperations(ctx, recovery, d.Service(conversation), owner, conversation)
		if recoveryErr != nil {
			c.noteFailure("Telegram day recovery unavailable", recoveryErr)
			data.OperationStatus = "unknown_outcome"
		}
		for opIndex := range ops {
			op := &ops[opIndex]
			if unresolvedDayStatus(op.Status) {
				data.OperationStatus = "unknown_outcome"
			}
		}
	}
	data.TextMode = strings.Contains(m.Text, " text")
	data.PublicTest = c.inlineEnabled()
	raw, marshalErr := json.Marshal(data)
	if marshalErr != nil {
		return
	}
	card.ItemsJSON = string(raw)
	if validateDayHTML() != nil {
		return
	}
	state, e := dayProjection(card, data)
	if e != nil {
		return
	}
	if e = d.Store.SaveDayCard(ctx, card); e != nil {
		return
	}
	if _, routeErr := c.resolveTarget(ctx, conversation); routeErr != nil {
		return
	}
	if data.TextMode {
		if c.sendDayText(ctx, card, data) == nil {
			delivered = true
		}
		return
	}
	msg, e := c.bot.SendWidget(ctx, &bot.SendWidgetParams{ChatID: card.Chat, MessageThreadID: card.Thread, HTML: dayHTML, ChatData: state, AllowQueries: true, Caption: dayText(data.Locale, "My day · /day text — text version", "Мой день · /day text — текстовая версия"), ProtectContent: true})
	if e != nil {
		if c.sendDayText(ctx, card, data) == nil {
			delivered = true
		}
		return
	}
	card.Message = msg.ID
	card.DesiredState = state
	card.DeliveredRevision = card.Revision
	if d.Store.SaveDayCard(ctx, card) == nil {
		delivered = true
	}
}
func (c *Channel) handleDayWidgetQuery(ctx context.Context, q *models.WidgetQuery) bool {
	if c.day == nil || !strings.HasPrefix(q.Data, "d:") {
		return false
	}
	ack := func(text string) {
		if _, ackErr := c.bot.AnswerWidgetQuery(ctx, &bot.AnswerWidgetQueryParams{WidgetQueryID: q.ID, Data: text}); ackErr != nil {
			c.noteFailure("Telegram day query acknowledgement failed", ackErr)
		}
	}
	return c.dispatchDayQuery(ctx, q, ack)
}
func (c *Channel) dispatchDayQuery(ctx context.Context, q *models.WidgetQuery, ack func(string)) bool {
	if c.day == nil {
		return false
	}
	if len(q.Data) > 64 || q.Chat == nil || q.Chat.Type != models.ChatTypePrivate || q.InlineMessageID != "" || q.Type.Type != models.WidgetQueryTypeTypeData {
		ack(dayText(q.From.LanguageCode, "Unavailable", "Недоступно"))
		return true
	}
	parts := strings.Split(q.Data, ":")
	if len(parts) != 5 {
		ack(dayText(q.From.LanguageCode, "Unavailable", "Недоступно"))
		return true
	}
	card, e := c.day.Store.GetDayCard(ctx, parts[1])
	if e != nil || card == nil || card.Instance != c.instanceID || card.Chat != q.Chat.ID || card.Message != q.MessageID {
		ack(dayText(q.From.LanguageCode, "Access denied", "Нет доступа"))
		return true
	}
	owner, e := c.dayOwner(ctx, q.From, q.Chat.ID, card.Thread)
	if e != nil || card.Owner != owner {
		ack(dayText(q.From.LanguageCode, "Access denied", "Нет доступа"))
		return true
	}
	if time.Now().After(card.Expires) {
		ack(dayText(q.From.LanguageCode, "This card expired. Open /day", "Карточка устарела. Откройте /day"))
		return true
	}
	rev, e := strconv.ParseInt(parts[2], 36, 64)
	idx, ie := strconv.Atoi(parts[4])
	if e != nil || ie != nil || idx < 0 || rev != card.Revision {
		ack(dayText(q.From.LanguageCode, "Data changed. Open /day", "Данные изменились. Откройте /day"))
		return true
	}
	verb := parts[3]
	if verb == "ack" {
		verb = "acknowledge"
	}
	if verb == "move" {
		verb = "reschedule"
	}
	if verb != "refresh" && verb != "page" && verb != "complete" && verb != "reschedule" && verb != "remind" && verb != "section" && verb != "details" && verb != "result" && verb != "publish" && verb != "acknowledge" {
		ack(dayText(q.From.LanguageCode, "Unavailable", "Недоступно"))
		return true
	}
	if !c.day.admit(owner) {
		ack(dayText(q.From.LanguageCode, "Too many requests. Try later.", "Слишком много запросов. Повторите позже."))
		return true
	}
	if (verb == "complete" || verb == "reschedule" || verb == "remind" || verb == "acknowledge") && !c.day.Actions {
		ack(dayText(q.From.LanguageCode, "Actions are disabled", "Действия отключены"))
		return true
	}
	ack(dayText(q.From.LanguageCode, "Request accepted", "Запрос принят"))
	c.wg.Add(1)
	go func() { defer c.wg.Done(); c.runDayAction(ctx, card, verb, idx) }()
	return true
}
func (c *Channel) runDayAction(root context.Context, card *dayview.Card, verb string, index int) {
	ctx, cancel := context.WithTimeout(root, 2*time.Minute)
	defer cancel()
	d := c.day
	var data cardData
	if json.Unmarshal([]byte(card.ItemsJSON), &data) != nil {
		return
	}
	loc, e := time.LoadLocation(data.Zone)
	if e != nil {
		return
	}
	if _, e = c.resolveTarget(ctx, data.Conversation); e != nil {
		return
	}
	if verb == "details" || verb == "result" {
		c.showDayDetail(ctx, card, data, index, verb == "result")
		return
	}
	if verb == "publish" {
		c.publishDayResult(ctx, card, data, index)
		return
	}
	if verb == "complete" || verb == "reschedule" || verb == "remind" || verb == "acknowledge" {
		if !d.Actions {
			return
		}
		var items []dayview.Item
		for _, s := range data.Sections {
			items = append(items, s.Items...)
		}
		if index < 0 || index >= len(items) {
			return
		}
		item := items[index]
		if verb == "complete" && !item.Complete || verb == "reschedule" && !item.Reschedule || verb == "acknowledge" && !item.ResolveDelivery {
			return
		}
		var due time.Time
		if verb == "remind" || verb == "reschedule" {
			due, e = c.pickDaySchedule(ctx, card, item, verb, data.Zone)
			if e != nil {
				return
			}
		}
		operationID := dayview.Revision(card.ID, strconv.FormatInt(card.Revision, 10), card.Owner, item.Source, item.ID, item.Revision, verb, due.UTC().Format(time.RFC3339))

		nonce := generateNonce()
		conf := &telegramConfirmer{channel: c, chatID: card.Chat, threadID: card.Thread, fromTg: card.Chat}
		dec, confirmErr := conf.Confirm(ctx, interact.Confirmation{Nonce: nonce, OperationID: operationID, Reason: interact.ConfirmMCP, Preview: dayActionPreview(data.Locale, item, verb, due, data.Zone), IssuedAt: time.Now(), ExpiresAt: time.Now().Add(90 * time.Second)}, interact.Actor{UserID: card.Owner, Channel: "telegram", ChannelInstance: c.instanceID, ExternalID: strconv.FormatInt(card.Chat, 10)})
		if confirmErr != nil || !dec.Approved {
			return
		}
		d.mu.Lock()
		if _, e = c.resolveTarget(ctx, data.Conversation); e != nil {
			d.mu.Unlock()
			c.sendSingleMessage(card.Chat, dayText(data.Locale, "Iulita is paused in this topic. No new action was started.", "Iulita приостановлена в этой теме. Новое действие не запускалось."), 0)
			return
		}
		currentCard, checkErr := d.Store.GetDayCard(ctx, card.ID)
		if checkErr != nil || currentCard == nil || currentCard.Revision != card.Revision || currentCard.Owner != card.Owner || currentCard.Instance != c.instanceID || currentCard.Chat != card.Chat || currentCard.Thread != card.Thread || currentCard.Message != card.Message || !time.Now().Before(currentCard.Expires) {
			d.mu.Unlock()
			return
		}
		if verb == "remind" {
			d.mu.Unlock()
			creator, ok := d.Store.(interface {
				CreateDayReminder(context.Context, string, string, string, string, time.Time, string) (int64, bool, error)
			})
			if !ok {
				return
			}
			_, _, createErr := creator.CreateDayReminder(ctx, card.Owner, data.Conversation, item.Title, data.Zone, due, operationID)
			if createErr != nil {
				c.sendSingleMessage(card.Chat, dayText(data.Locale, "Could not create reminder", "Не удалось создать напоминание"), 0, card.Thread)
				return
			}
		} else {
			command, marshalErr := json.Marshal(dayview.Command{Item: item, Action: verb, Due: due})
			if marshalErr != nil {
				d.mu.Unlock()
				return
			}
			op := &dayview.Operation{ID: operationID, CardID: card.ID, Owner: card.Owner, Conversation: data.Conversation, Instance: c.instanceID, CommandJSON: string(command), ApprovedAt: time.Now(), Status: "running", CreatedAt: time.Now()}
			claimed, claimErr := d.Store.ClaimDayOperation(ctx, op)
			d.mu.Unlock()
			if claimErr != nil || !claimed {
				notice := dayText(data.Locale, "Operation already accepted. Refresh the card; do not repeat the action.", "Операция уже принята. Обновите карточку; повторять действие не нужно.")
				if claimErr != nil {
					notice = dayText(data.Locale, "Request was not started. Review uncertain operations in Jobs, or refresh your day.", "Запрос не запущен. Проверьте неопределённые операции в разделе «Задания» или обновите день.")
				}
				c.sendSingleMessage(card.Chat, notice, 0, card.Thread)
				return
			}
			e = d.Service(data.Conversation).Change(ctx, card.Owner, item, verb, due)
			status := "succeeded"
			if e != nil {
				status = "unknown_outcome"
				data.OperationStatus = status
			}
			if d.Store.FinishDayOperation(ctx, op.ID, status) != nil {
				return
			}
			if e != nil {
				c.sendSingleMessage(card.Chat, dayText(data.Locale, "Checking the result. Do not repeat the action; check the source first.", "Результат не подтверждён. Не повторяйте действие; сначала проверьте источник."), 0, card.Thread)
			}
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	current, e := d.Store.GetDayCard(ctx, card.ID)
	if e != nil || current == nil {
		return
	}
	card = current
	outcome := data.OperationStatus
	if json.Unmarshal([]byte(current.ItemsJSON), &data) != nil {
		return
	}
	if outcome != "" {
		data.OperationStatus = outcome
	}
	data.Actions = d.Actions
	switch verb {
	case "section":
		if index < 0 || index > 3 {
			return
		}
		data.Section = index
		data.Page = 0
	case "page":
		data.Page = index
	default:
		oldSections, marshalErr := json.Marshal(data.Sections)
		if marshalErr != nil {
			return
		}
		oldOutcome := data.OperationStatus
		data.Sections = d.Service(data.Conversation).Read(ctx, card.Owner, time.Now(), loc)
		sanitizeDaySections(data.Sections)
		if recovery, ok := d.Store.(dayview.RecoveryStore); ok {
			ops, recoveryErr := dayview.RecoverOperations(ctx, recovery, d.Service(data.Conversation), card.Owner, data.Conversation)
			if recoveryErr != nil {
				c.noteFailure("Telegram day recovery unavailable", recoveryErr)
				return
			}
			data.OperationStatus = ""
			for opIndex := range ops {
				op := &ops[opIndex]
				if unresolvedDayStatus(op.Status) {
					data.OperationStatus = "unknown_outcome"
				}
			}
		}
		newSections, marshalErr := json.Marshal(data.Sections)
		if marshalErr != nil {
			return
		}
		if verb == "refresh" && bytes.Equal(oldSections, newSections) && oldOutcome == data.OperationStatus {
			return
		}
	}
	card.Revision++
	raw, marshalErr := json.Marshal(data)
	if marshalErr != nil {
		return
	}
	card.ItemsJSON = string(raw)
	state, e := dayProjection(card, data)
	if e != nil {
		return
	}
	card.DesiredState = state
	if d.Store.SaveDayCard(ctx, card) != nil {
		return
	}
	// The outbox is the only editor: direct edits race with replay after restart.
}

func daySection(source string) int {
	switch strings.ToLower(source) {
	case "reminders":
		return 1
	case "calendar", "events":
		return 2
	case "jobs", "operations":
		return 3
	default:
		return 0
	}
}
func dayCategoryLabel(locale string, section int) string {
	switch section {
	case 1:
		return dayText(locale, "Reminders", "Напоминания")
	case 2:
		return dayText(locale, "Events", "События")
	case 3:
		return dayText(locale, "Jobs", "Задания")
	default:
		return dayText(locale, "Tasks", "Задачи")
	}
}

func (d *DayController) admit(owner string) bool {
	d.once.Do(func() { d.limiter = ratelimit.New(5, 5*time.Second) })
	return d.limiter.Allow(owner)
}

func unresolvedDayStatus(status string) bool {
	switch status {
	case "unknown_outcome", "running", "inconclusive", "not_observed", "revoked":
		return true
	}
	return false
}

func dayDueSummary(i dayview.Item, locale string) string {
	if i.Deadline != "" && i.Deadline != i.Due {
		return dayText(locale, "Deadline: ", "Срок: ") + i.Deadline
	}
	return i.Due
}

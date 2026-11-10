package telegram

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/iulita-ai/iulita/internal/dayview"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/i18n"
	"github.com/iulita-ai/iulita/internal/security"
	"github.com/iulita-ai/iulita/internal/skill/interact"
)

const topicHelp = "Темы / Topics: /topic — список тем / list topics; сообщение в общем чате начинает новую тему / a root message starts a new topic; /topic new NAME; /topic link; /topic help; /topic rename NAME; /topic pause; /topic resume; /topic delete; /topic bind — подтвердить бота для старой темы / verify bot binding for a legacy topic. В теме / In a topic: /clear — очистить контекст / clear context; /compact — сжать контекст / compact context. Пауза отключает обработку Iulita, тема Telegram остаётся открытой. Из основного чата / From root: /topic resume THREAD_ID. Восстановить создание / Recover creation inside the topic: /topic recover OPERATION_ID"

// Historical close/reopen commands are aliases for local Iulita pause/resume.
// Telegram's native close/reopen API supports supergroups only, never DMs.
func localTopicAction(action string) string {
	switch action {
	case "close":
		return "pause"
	case "reopen":
		return "resume"
	}
	return action
}

type topicStore interface {
	EnsureTelegramTopics(context.Context) error
	ApplyLocalTelegramTopicOperation(context.Context, *domain.TelegramTopicOperation, *domain.TelegramTopicState) error
	GetTelegramTopicOperation(context.Context, string, string, string) (*domain.TelegramTopicOperation, error)
	GetTelegramTopicState(context.Context, string) (*domain.TelegramTopicState, error)
	PutTelegramTopicState(context.Context, *domain.TelegramTopicState) error
	ClaimTelegramTopicOperation(context.Context, *domain.TelegramTopicOperation) (*domain.TelegramTopicOperation, bool, error)
	SaveTelegramTopicOperation(context.Context, *domain.TelegramTopicOperation) error
}

func parseTopicCommand(text string) (command, argument string, recognized bool) {
	f := strings.Fields(text)
	if len(f) == 1 {
		return "list", "", true
	}
	if len(f) == 0 {
		return "", "", false
	}
	action := localTopicAction(f[1])
	switch action {
	case "new", "rename", "pause", "resume", "delete", "recover", "help", "link", "list", "bind":
	default:
		return "", "", false
	}
	name := ""
	if len(f) > 2 {
		name = strings.Join(f[2:], " ")
	}
	return action, name, true
}
func (c *Channel) handleTopicLifecycle(ctx context.Context, m *models.Message, key, owner string) bool {
	fields := strings.Fields(m.Text)
	if len(fields) == 0 {
		return false
	}
	cmd, ok := c.commandName(fields[0])
	if !ok || cmd != "/topic" {
		return false
	}
	action, name, ok := parseTopicCommand(m.Text)
	if !ok {
		c.sendSingleMessage(m.Chat.ID, topicHelp, m.ID, m.MessageThreadID)
		return true
	}
	notice := func(v string) { c.sendSingleMessage(m.Chat.ID, v, m.ID, m.MessageThreadID) }
	if m.From == nil || m.Chat.Type != models.ChatTypePrivate || m.From.ID != m.Chat.ID || owner == "" {
		notice("Управление темами доступно только владельцу в личном чате / Private owner chat required")
		return true
	}
	if action == "help" {
		notice(topicHelp)
		return true
	}
	if action == "link" {
		link, err := c.TopicLink(ctx, key, owner)
		if err != nil {
			link = "Откройте нужную тему и отправьте /topic link / Open a topic and send /topic link"
		}
		notice(link)
		return true
	}
	store, ok := c.store.(topicStore)
	if !ok || store.EnsureTelegramTopics(ctx) != nil {
		notice("Хранилище тем недоступно / Topic storage unavailable")
		return true
	}
	if action == "list" {
		source := *m
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			c.noteFailure("Telegram topic reconciliation failed", c.ReconcileTopicDeletions(ctx))
			c.listTopics(ctx, &source, owner, name)
		}()
		return true
	}
	if (action == "resume" || action == "bind") && m.MessageThreadID == 0 {
		id, e := strconv.Atoi(name)
		if e != nil || id <= 0 {
			notice("Укажите номер темы: /topic " + action + " THREAD_ID / Specify the topic number")
			return true
		}
		key = c.routeKey(m.Chat.ID, id)
	}
	if action != "new" && m.MessageThreadID == 0 && ((action != "resume" && action != "bind") || name == "") {
		notice("Откройте нужную тему / Open the target topic first")
		return true
	}
	if (action == "new" && name != "") || action == "rename" {
		if err := validateTopicName(name); err != nil {
			notice(err.Error())
			return true
		}
	}
	// Capture the source command identity, independent of later updates.
	source := *m
	actor := *m.From
	source.From = &actor
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		if action == "new" && name == "" {
			var err error
			name, err = c.askTopicName(ctx, &source)
			if err != nil {
				return
			}
		}
		c.runTopicLifecycle(ctx, &source, key, owner, action, name)
	}()
	return true
}

func validateTopicName(name string) error {
	if strings.TrimSpace(name) != name || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 128 || strings.HasPrefix(name, "/") || strings.ContainsFunc(name, unicode.IsControl) {
		return fmt.Errorf("название: 1–128 символов в одну строку / Name must contain 1–128 characters on one line")
	}
	if matched, _ := security.Contains(name); matched || security.RedactPaymentData(name) != name {
		return fmt.Errorf("не используйте секреты или реквизиты в названии / Sensitive topic name rejected")
	}
	return nil
}

func (c *Channel) askTopicName(root context.Context, m *models.Message) (string, error) {
	ctx, cancel := context.WithTimeout(root, interact.DefaultTimeout)
	defer cancel()
	ctx = i18n.WithLocale(ctx, i18n.ResolveLocale("", m.From.LanguageCode))
	p := &telegramPrompter{channel: c, chatID: m.Chat.ID, threadID: m.MessageThreadID, actor: m.From.ID, prompts: c.prompts}
	notice := func(text string) { c.sendSingleMessage(m.Chat.ID, text, 0, m.MessageThreadID) }
	question := "Как назвать новую тему? Отправьте название (1–128 символов) или /cancel.\nWhat should the new topic be called? Send a name or /cancel."
	for attempt := 0; attempt < 3; attempt++ {
		name, err := p.Ask(ctx, question, nil)
		if err != nil {
			switch {
			case errors.Is(err, errPromptPending):
				notice("Уже ожидаю ответ в этом чате. Завершите его или отправьте /cancel / Finish the pending question or send /cancel")
			case errors.Is(err, interact.ErrCancelled), errors.Is(err, interact.ErrPromptTimeout):
				notice("Создание темы отменено / Topic creation cancelled")
			default:
				notice("Не удалось запросить название / Could not request a topic name")
			}
			return "", err
		}
		name = strings.TrimSpace(name)
		if err = validateTopicName(name); err == nil {
			return name, nil
		}
		question = err.Error() + "\nОтправьте другое название или /cancel / Send another name or /cancel"
	}
	notice("Создание темы отменено. Начать заново: /topic new / Topic creation cancelled; restart with /topic new")
	return "", interact.ErrCancelled
}

func (c *Channel) runTopicLifecycle(root context.Context, m *models.Message, key, owner, action, name string) {
	action = localTopicAction(action)
	ctx, cancel := context.WithTimeout(root, 2*time.Minute)
	defer cancel()
	store, storeOK := c.store.(topicStore)
	if !storeOK {
		return
	}
	notice := func(v string) {
		thread := m.MessageThreadID
		if action == "pause" || action == "delete" {
			thread = 0
		}
		c.sendSingleMessage(m.Chat.ID, v, 0, thread)
	}
	if action == "new" || action == "rename" {
		if err := validateTopicName(name); err != nil {
			notice(err.Error())
			return
		}
	}
	targetThread := m.MessageThreadID
	if action == "new" {
		r, err := c.resolveTargetForLifecycle(ctx, key)
		if err != nil || !r.Private || r.OwnerID != owner || r.ChatID != m.Chat.ID || r.ThreadID != m.MessageThreadID {
			notice("Нет доступа к теме / Topic access denied")
			return
		}
	}
	if action != "new" {
		r, err := c.resolveTargetForLifecycle(ctx, key)
		if err != nil || !r.Private || r.OwnerID != owner || r.ChatID != m.Chat.ID || (r.ThreadID != m.MessageThreadID && ((action != "resume" && action != "bind") || m.MessageThreadID != 0)) {
			notice("Нет доступа к теме / Topic access denied")
			return
		}
		targetThread = r.ThreadID
		if action == "delete" && r.BotID == 0 {
			notice("Для этой старой темы ещё не подтверждён бот. Сначала выполните /topic bind в теме; контекст пока сохранён. / Confirm the legacy bot binding with /topic bind first; context preserved.")
			return
		}
		if action == "bind" && (r.BotID != 0 || c.botID <= 0) {
			notice("Тема уже привязана к боту или подтверждённый бот недоступен / Topic already bound or verified bot unavailable")
			return
		}
	}
	fingerprint := dayview.Revision(c.instanceID, strconv.FormatInt(c.botID, 10), owner, strconv.FormatInt(m.Chat.ID, 10), strconv.Itoa(targetThread), action, name)
	id := dayview.Revision(fingerprint, strconv.Itoa(m.ID))
	preview := fmt.Sprintf("%s · topic %d\n%s", action, targetThread, name)
	if action == "new" {
		preview = fmt.Sprintf("Создать новую тему «%s»? / Create a new topic named «%s»?", name, name)
	}
	if action == "bind" {
		preview = fmt.Sprintf("Привязать сохранённый контекст старой темы %d к текущему боту @%s (ID %d)? Старая версия не сохранила ID бота, поэтому историческая привязка неизвестна. Подтверждение включит синхронизацию удаления: если проверка обнаружит, что тема уже удалена, контекст очистится и связанные напоминания и фоновые задания отменятся. Внешние задачи сохранятся. Сама привязка сейчас не удаляет историю. / Associate legacy context of unknown bot provenance with this verified bot and enable deletion synchronization; an already deleted topic will then be cleaned up.", targetThread, c.linkUsername(), c.botID)
	}
	if action == "pause" {
		preview = fmt.Sprintf("Приостановить обработку и доставку Iulita в теме %d? Тема Telegram останется открытой. Уже отправленные внешние операции не отменяются; новые ответы Iulita блокируются. Возобновить: /topic resume %d из основного чата. / Pause Iulita processing and delivery in topic %d; Telegram topic stays open.", targetThread, targetThread, targetThread)
	}
	if action == "resume" {
		preview = fmt.Sprintf("Возобновить обработку Iulita в теме %d? Тема Telegram не изменяется; пропущенные сообщения не отправляются повторно. / Resume Iulita in topic %d; no Telegram change or replay of skipped messages.", targetThread, targetThread)
	}
	if action == "delete" {
		nameLabel := ""
		if state, e := store.GetTelegramTopicState(ctx, key); e == nil && state != nil && state.Name != "" {
			nameLabel = " «" + truncateRunes(security.RedactSensitiveText(state.Name), 128) + "»"
		}
		preview = fmt.Sprintf("Удалить тему %d%s и все её сообщения в Telegram? Контекст и конспекты Iulita будут удалены; привязанные напоминания и фоновые задания отменены. Отменить удаление нельзя. Внешние задачи Todoist, Google Tasks и Craft сохранятся. Уже отправленные внешние запросы могут завершиться. / Permanently delete topic %d, clear its Iulita context and cancel bound reminders/background work; external tasks are preserved.", targetThread, nameLabel, targetThread)
	}
	conf := &telegramConfirmer{channel: c, chatID: m.Chat.ID, threadID: m.MessageThreadID, fromTg: m.From.ID}
	decision, err := conf.Confirm(ctx, interact.Confirmation{Nonce: generateNonce(), OperationID: id, Reason: interact.ConfirmMCP, Preview: preview, IssuedAt: time.Now(), ExpiresAt: time.Now().Add(90 * time.Second)}, interact.Actor{UserID: owner, Channel: "telegram", ChannelInstance: c.instanceID, ExternalID: strconv.FormatInt(m.From.ID, 10)})
	if err != nil || !decision.Approved {
		return
	}
	c.topicMutationMu.Lock()
	defer c.topicMutationMu.Unlock()
	if ctx.Err() != nil {
		return
	}
	if action == "new" {
		r, e := c.resolveTargetForLifecycle(ctx, key)
		if e != nil || !r.Private || r.OwnerID != owner || r.ChatID != m.Chat.ID || r.ThreadID != m.MessageThreadID {
			notice("Нет доступа к теме / Topic access denied")
			return
		}
	}
	if action == "recover" {
		op, e := store.GetTelegramTopicOperation(ctx, name, owner, c.instanceID)
		if e != nil || op == nil || op.Action != "new" || op.ChatID != m.Chat.ID || targetThread == 0 || op.Status == "succeeded" || (op.ThreadID != 0 && op.ThreadID != targetThread) {
			notice("Не удалось связать операцию с этой темой / Recovery operation unavailable for this topic")
			return
		}
		op.ThreadID = targetThread
		op.Status = "created"
		if store.SaveTelegramTopicOperation(ctx, op) != nil {
			return
		}
		if c.materializeCreatedTopic(ctx, store, op) != nil {
			notice("Не удалось восстановить маршрут / Route recovery failed")
		}
		return
	}
	if action != "new" {
		latest, e := store.GetTelegramTopicState(ctx, key)
		if e != nil || latest != nil && latest.Status == "deleted" {
			notice("Тема удалена или недоступна / Topic is deleted or unavailable")
			return
		}
	}
	requested := &domain.TelegramTopicOperation{BotID: c.botID, ID: id, Digest: fingerprint, OwnerID: owner, InstanceID: c.instanceID, ChatID: m.Chat.ID, ThreadID: targetThread, Action: action, Name: name}
	op, claimed, err := store.ClaimTelegramTopicOperation(ctx, requested)
	if err != nil {
		notice("Операция не принята / Operation unavailable")
		return
	}
	if !claimed {
		if op.Status == "created" && op.ThreadID > 0 {
			if materializeErr := c.materializeCreatedTopic(ctx, store, op); materializeErr != nil {
				c.noteFailure("Telegram created-topic materialization failed", materializeErr)
			}
			return
		}
		notice("Операция уже принята или её результат неизвестен. Проверьте Telegram перед повтором. В созданной теме: /topic recover " + op.ID + " / Already accepted or outcome unknown; check Telegram before retrying")
		return
	}
	if action == "bind" {
		binder, ok := c.store.(interface {
			BindLegacyTelegramTopic(context.Context, domain.TelegramTopicDeletionScope) error
		})
		if !ok || c.verifyTopicIdentity(ctx) != nil {
			op.Status = "failed"
			if deliveryErr := store.SaveTelegramTopicOperation(ctx, op); deliveryErr != nil {
				c.noteFailure("Telegram SaveTelegramTopicOperation failed", deliveryErr)
			}
			notice("Не удалось подтвердить бота. Привязка не изменена / Bot verification unavailable; binding unchanged")
			return
		}
		scope := domain.TelegramTopicDeletionScope{BotID: c.botID, OperationID: op.ID, Digest: op.Digest, ConversationID: key, InstanceID: c.instanceID, OwnerID: owner, ChatID: m.Chat.ID, ThreadID: targetThread}
		if e := binder.BindLegacyTelegramTopic(ctx, scope); e != nil {
			readback, re := store.GetTelegramTopicOperation(ctx, op.ID, owner, c.instanceID)
			route, rr := c.resolveTargetForLifecycle(ctx, key)
			if re != nil || rr != nil || readback == nil || readback.Status != "succeeded" || route.BotID != c.botID {
				if re == nil && rr == nil && readback != nil && readback.Status == "running" && route.BotID == 0 {
					op.Status = "failed"
					if deliveryErr := store.SaveTelegramTopicOperation(ctx, op); deliveryErr != nil {
						c.noteFailure("Telegram SaveTelegramTopicOperation failed", deliveryErr)
					}
					notice("Не удалось привязать старую тему. Контекст сохранён / Legacy binding failed; context preserved")
				} else {
					op.Status = "unknown_outcome"
					if deliveryErr := store.SaveTelegramTopicOperation(ctx, op); deliveryErr != nil {
						c.noteFailure("Telegram SaveTelegramTopicOperation failed", deliveryErr)
					}
					notice("Результат привязки не подтверждён. Проверьте /topic перед повтором / Binding outcome unconfirmed; check /topic before retrying")
				}
				return
			}
		}
		notice("Тема привязана к подтверждённому боту. Контекст сохранён; синхронизация удаления включена / Topic bound; context preserved; deletion synchronization enabled")
		return
	}
	if action == "delete" {
		c.executeTopicDeletion(ctx, key, op)
		return
	}
	if action == "pause" || action == "resume" {
		prior, e := store.GetTelegramTopicState(ctx, key)
		if e != nil {
			op.Status = "failed"
			if deliveryErr := store.SaveTelegramTopicOperation(ctx, op); deliveryErr != nil {
				c.noteFailure("Telegram SaveTelegramTopicOperation failed", deliveryErr)
			}
			notice("Не удалось прочитать локальное состояние / Local state unavailable")
			return
		}
		state := &domain.TelegramTopicState{ConversationID: key, Status: "paused"}
		if prior != nil {
			state.Name = prior.Name
		}
		if action == "resume" {
			state.Status = "open"
		}
		if e = store.ApplyLocalTelegramTopicOperation(ctx, op, state); e != nil {
			op.Status = "failed"
			if deliveryErr := store.SaveTelegramTopicOperation(ctx, op); deliveryErr != nil {
				c.noteFailure("Telegram SaveTelegramTopicOperation failed", deliveryErr)
			}
			notice("Не удалось сохранить локальное состояние Iulita. Тема Telegram не изменена / Could not save local Iulita state; Telegram topic unchanged")
			return
		}
		if action == "pause" {
			notice(fmt.Sprintf("Iulita приостановлена в теме %d. Тема Telegram остаётся открытой. Возобновить из основного чата: /topic resume %d / Iulita paused; Telegram topic stays open.", targetThread, targetThread))
		} else {
			notice("Iulita возобновлена. Пропущенные сообщения не повторяются; тема Telegram не изменена / Iulita resumed; no replay, Telegram topic unchanged")
		}
		return
	}
	switch action {
	case "new":
		created, e := c.bot.CreateForumTopic(ctx, &bot.CreateForumTopicParams{ChatID: m.Chat.ID, Name: name})
		err = e
		if err == nil {
			op.ThreadID = created.MessageThreadID
			op.Status = "created"
			if store.SaveTelegramTopicOperation(ctx, op) != nil {
				notice("Тема могла быть создана. Проверьте Telegram; не повторяйте создание. В созданной теме: /topic recover " + op.ID + " / Creation outcome unconfirmed; check Telegram")
				return
			}
			if c.materializeCreatedTopic(ctx, store, op) != nil {
				notice("Тема создана, локальный маршрут восстанавливается повтором той же команды / Topic created; repeat the same operation to restore its route")
			}
			return
		}
	case "rename":
		_, err = c.bot.EditForumTopic(ctx, &bot.EditForumTopicParams{ChatID: m.Chat.ID, MessageThreadID: targetThread, Name: name})
	}
	if err != nil {
		op.Status = "unknown_outcome"
		if deliveryErr := store.SaveTelegramTopicOperation(ctx, op); deliveryErr != nil {
			c.noteFailure("Telegram SaveTelegramTopicOperation failed", deliveryErr)
		}
		notice("Результат не подтверждён. Проверьте тему в Telegram. Для созданной темы: /topic recover " + op.ID + " / Outcome unconfirmed; check Telegram before retrying")
		return
	}
	prior, stateErr := store.GetTelegramTopicState(ctx, key)
	if stateErr != nil {
		c.noteFailure("Telegram topic state readback failed", stateErr)
		return
	}
	state := &domain.TelegramTopicState{ConversationID: key, Status: "open"}
	if prior != nil {
		state.Name = prior.Name
		state.Status = prior.Status
	}
	if action == "rename" {
		state.Name = name
	}
	if action == "delete" {
		state.Status = "deleted"
	}
	if store.PutTelegramTopicState(ctx, state) != nil {
		op.Status = "unknown_outcome"
	} else {
		op.Status = "succeeded"
	}
	if deliveryErr := store.SaveTelegramTopicOperation(ctx, op); deliveryErr != nil {
		c.noteFailure("Telegram SaveTelegramTopicOperation failed", deliveryErr)
		notice("Telegram принял операцию, локальное состояние не подтверждено / Local state unconfirmed")
		return
	}
	if op.Status == "succeeded" {
		notice("Операция выполнена / Topic operation completed")
	} else {
		notice("Telegram принял операцию, локальное состояние не подтверждено / Local state unconfirmed")
	}
}
func (c *Channel) materializeCreatedTopic(ctx context.Context, store topicStore, op *domain.TelegramTopicOperation, sendNotice ...bool) error {
	m := &models.Message{Chat: models.Chat{ID: op.ChatID, Type: models.ChatTypePrivate}, From: &models.User{ID: op.ChatID}, MessageThreadID: op.ThreadID}
	key, conversationErr := c.conversationFor(ctx, m)
	if conversationErr != nil {
		return conversationErr
	}
	if err := c.persistRoute(ctx, key, m, op.OwnerID); err != nil {
		return err
	}
	if err := store.PutTelegramTopicState(ctx, &domain.TelegramTopicState{ConversationID: key, Name: op.Name, Status: "open"}); err != nil {
		return err
	}
	op.Status = "succeeded"
	if err := store.SaveTelegramTopicOperation(ctx, op); err != nil {
		return err
	}
	if len(sendNotice) == 0 || sendNotice[0] {
		link, linkErr := c.TopicLink(ctx, key, op.OwnerID)
		if linkErr != nil {
			return linkErr
		}
		c.sendSingleMessage(op.ChatID, "Тема создана / Topic created: "+link, 0)
	}
	return nil
}

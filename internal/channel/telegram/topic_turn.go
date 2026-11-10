package telegram

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/dayview"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/security"
)

type topicSeedStore interface {
	ClaimTelegramTopicSeed(context.Context, *domain.TelegramTopicSeed) (bool, error)
	TransitionTelegramTopicSeed(context.Context, *domain.TelegramTopicSeed, string, string) error
	FinishTelegramTopicSeed(context.Context, string, string, int64, int, int, string) error
	FindTelegramTopicAlbum(context.Context, string, string, int64, string) (*domain.TelegramTopicSeed, error)
}

func (c *Channel) finishTopicSeed(root context.Context, msg channel.IncomingMessage, status string) {
	s, ok := c.store.(topicSeedStore)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(root), 5*time.Second)
	defer cancel()
	target, e := c.resolveTargetForLifecycle(ctx, msg.ChatID)
	if e != nil || !target.Private || target.OwnerID != msg.ResolvedUserID || target.ThreadID <= 0 {
		return
	}
	ids := msg.RelatedMessageIDs
	if len(ids) == 0 {
		ids = []int{msg.MessageID}
	}
	for _, id := range ids {
		if deliveryErr := s.FinishTelegramTopicSeed(ctx, c.instanceID, msg.ResolvedUserID, target.ChatID, target.ThreadID, id, status); deliveryErr != nil {
			c.noteFailure("Telegram FinishTelegramTopicSeed failed", deliveryErr)
		}
	}
}

// prepareTopicTurn starts a separate conversation for each ordinary root turn.
// It never retries uncertain Telegram writes or an already accepted seed.
func (c *Channel) prepareTopicTurn(root context.Context, m *models.Message, msg *channel.IncomingMessage) bool {
	if !c.topicsEnabled || m.MessageThreadID != 0 || strings.HasPrefix(strings.TrimSpace(msg.Text), "/") {
		return true
	}
	if m.Chat.Type != models.ChatTypePrivate {
		return true
	}
	if m.From == nil || m.From.ID != m.Chat.ID || !c.isAllowed(m.From.ID) {
		return false
	}
	if msg.ResolvedUserID == "" {
		c.sendSingleMessage(m.Chat.ID, "Не удалось определить владельца новой темы / Topic owner unavailable", m.ID)
		return false
	}
	ctx, cancel := context.WithTimeout(root, 30*time.Second)
	defer cancel()
	notice := func(text string) { c.sendSingleMessage(m.Chat.ID, text, m.ID) }
	store, ok := c.store.(topicStore)
	seeds, seedOK := c.store.(topicSeedStore)
	if !ok || !seedOK || store.EnsureTelegramTopics(ctx) != nil {
		notice("Не удалось подготовить новую тему. Сообщение не обработано / Could not prepare a topic; message not processed")
		return false
	}
	c.topicMutationMu.Lock()
	defer c.topicMutationMu.Unlock()
	r, err := c.resolveTargetForLifecycle(ctx, msg.ChatID)
	if err != nil || !r.Private || r.OwnerID != msg.ResolvedUserID || r.ChatID != m.Chat.ID || r.ThreadID != 0 {
		return false
	}
	id := dayview.Revision(c.instanceID, msg.ResolvedUserID, strconv.FormatInt(m.Chat.ID, 10), "root-topic-seed", strconv.Itoa(m.ID))
	seed := &domain.TelegramTopicSeed{ID: id, OwnerID: msg.ResolvedUserID, InstanceID: c.instanceID, ChatID: m.Chat.ID, MessageID: m.ID, AlbumID: m.MediaGroupID}
	album, err := seeds.FindTelegramTopicAlbum(ctx, c.instanceID, msg.ResolvedUserID, m.Chat.ID, m.MediaGroupID)
	if err != nil {
		return false
	}
	claimed, err := seeds.ClaimTelegramTopicSeed(ctx, seed)
	if err != nil || !claimed {
		notice("Это сообщение уже принято или результат создания темы неизвестен. Проверьте /topic; автоматического повтора не будет / Already accepted or outcome unconfirmed; check /topic")
		return false
	}
	if album != nil {
		target, e := c.resolveTarget(ctx, c.routeKey(m.Chat.ID, album.ThreadID))
		if album.ThreadID <= 0 || e != nil || !target.Private || target.OwnerID != seed.OwnerID || target.ThreadID != album.ThreadID {
			notice("Тема альбома недоступна или результат её создания неизвестен. Проверьте /topic / Album topic unavailable; check /topic")
			return false
		}
		seed.ThreadID = album.ThreadID
		if seeds.TransitionTelegramTopicSeed(ctx, seed, "creating", "created") != nil {
			return false
		}
	} else {
		op := &domain.TelegramTopicOperation{ID: id, Digest: id, OwnerID: seed.OwnerID, InstanceID: seed.InstanceID, ChatID: seed.ChatID, Action: "new", Name: topicTitle(msg.Text)}
		op, claimed, err = store.ClaimTelegramTopicOperation(ctx, op)
		if err != nil || !claimed {
			return false
		}
		created, createErr := c.bot.CreateForumTopic(ctx, &bot.CreateForumTopicParams{ChatID: m.Chat.ID, Name: op.Name})
		if createErr != nil || created.MessageThreadID <= 0 {
			op.Status = "unknown_outcome"
			if deliveryErr := store.SaveTelegramTopicOperation(ctx, op); deliveryErr != nil {
				c.noteFailure("Telegram SaveTelegramTopicOperation failed", deliveryErr)
			}
			if deliveryErr := seeds.TransitionTelegramTopicSeed(ctx, seed, "creating", "unknown_create"); deliveryErr != nil {
				c.noteFailure("Telegram TransitionTelegramTopicSeed failed", deliveryErr)
			}
			notice("Результат создания темы не подтверждён. Проверьте Telegram перед повтором / Topic creation unconfirmed; check Telegram before retrying")
			return false
		}
		op.ThreadID = created.MessageThreadID
		op.Status = "created"
		seed.ThreadID = created.MessageThreadID
		if store.SaveTelegramTopicOperation(ctx, op) != nil || seeds.TransitionTelegramTopicSeed(ctx, seed, "creating", "created") != nil || c.materializeCreatedTopic(ctx, store, op, false) != nil {
			notice("Тема создана, но её подготовка не завершена. В созданной теме: /topic recover " + id + " / Topic created; setup incomplete")
			return false
		}
	}
	if seeds.TransitionTelegramTopicSeed(ctx, seed, "created", "copying") != nil {
		return false
	}
	copied, err := c.bot.CopyMessage(ctx, &bot.CopyMessageParams{ChatID: m.Chat.ID, FromChatID: m.Chat.ID, MessageID: m.ID, MessageThreadID: seed.ThreadID, ProtectContent: true})
	if err != nil || copied.ID <= 0 {
		if deliveryErr := seeds.TransitionTelegramTopicSeed(ctx, seed, "copying", "unknown_copy"); deliveryErr != nil {
			c.noteFailure("Telegram TransitionTelegramTopicSeed failed", deliveryErr)
		}
		notice("Тема создана, но перенос первого сообщения не подтверждён. Откройте её через /topic и отправьте сообщение там / Topic created; seed copy unconfirmed; open it using /topic and send your message there")
		return false
	}
	seed.CopyID = copied.ID
	if seeds.TransitionTelegramTopicSeed(ctx, seed, "copying", "copied") != nil || seeds.TransitionTelegramTopicSeed(ctx, seed, "copied", "processing") != nil {
		notice("Сообщение перенесено в тему, но обработка не запущена. Продолжите в теме / Message copied; processing not started; continue in the topic")
		return false
	}
	msg.ChatID = c.routeKey(m.Chat.ID, seed.ThreadID)
	msg.MessageID = seed.CopyID
	c.RecordTurnSender(msg.ChatID, m.From.ID)
	if album == nil {
		link, linkErr := c.TopicLink(ctx, msg.ChatID, seed.OwnerID)
		if linkErr != nil {
			c.noteFailure("Telegram created-topic link unavailable", linkErr)
			link = "/topic"
		}
		notice("Тема создана, первое сообщение перенесено. Ответ будет в теме / Topic created; message copied; reply will be in the topic: " + link)
	}
	if msg.Quote != nil {
		msg.Quote.Verified = false
		msg.Quote.SourceMessageID = 0
	}
	return true
}

func topicTitle(text string) string {
	firstLine, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	title := strings.Join(strings.Fields(security.RedactSensitiveText(firstLine)), " ")
	title = truncateRunes(title, 128)
	if validateTopicName(title) != nil {
		return "Новая тема / New topic"
	}
	return title
}

func (c *Channel) recordTopicMetadata(ctx context.Context, m *models.Message, key, owner string) {
	if m.MessageThreadID <= 0 || m.From == nil || m.Chat.Type != models.ChatTypePrivate || m.From.ID != m.Chat.ID || owner == "" {
		return
	}
	s, ok := c.store.(topicStore)
	if !ok || s.EnsureTelegramTopics(ctx) != nil {
		return
	}
	state, e := s.GetTelegramTopicState(ctx, key)
	if e != nil {
		return
	}
	if state == nil {
		state = &domain.TelegramTopicState{ConversationID: key, Status: "open"}
	}
	name := ""
	if m.ForumTopicCreated != nil {
		name = m.ForumTopicCreated.Name
	}
	if m.ForumTopicEdited != nil {
		name = m.ForumTopicEdited.Name
	}
	if name != "" {
		state.Name = topicTitle(name)
	}
	if deliveryErr := s.PutTelegramTopicState(ctx, state); deliveryErr != nil {
		c.noteFailure("Telegram PutTelegramTopicState failed", deliveryErr)
	}
}

func (c *Channel) listTopics(ctx context.Context, m *models.Message, owner, pageText string) {
	page := 1
	if pageText != "" {
		var e error
		page, e = strconv.Atoi(pageText)
		if e != nil || page < 1 || page > 100000 {
			c.sendSingleMessage(m.Chat.ID, "/topic list PAGE", m.ID, m.MessageThreadID)
			return
		}
	}
	s, ok := c.store.(interface {
		ListTelegramTopics(context.Context, string, string, int64, int, int, ...int64) ([]domain.TelegramTopicListItem, error)
	})
	if !ok {
		c.sendSingleMessage(m.Chat.ID, "Список тем недоступен / Topic list unavailable", m.ID, m.MessageThreadID)
		return
	}
	rows, e := s.ListTelegramTopics(ctx, c.instanceID, owner, m.Chat.ID, (page-1)*20, 21, c.botID)
	if e != nil {
		c.sendSingleMessage(m.Chat.ID, "Не удалось загрузить список тем / Could not load topics", m.ID, m.MessageThreadID)
		return
	}
	var text strings.Builder
	fmt.Fprintf(&text, "Ваши темы · страница %d / Your topics · page %d\n", page, page)
	more := len(rows) > 20
	if more {
		rows = rows[:20]
	}
	if len(rows) == 0 {
		text.WriteString("Пока нет известных тем на этой странице / No known topics on this page\n")
	}
	for _, row := range rows {
		name := security.RedactSensitiveText(row.Name)
		if name == "" {
			name = fmt.Sprintf("Тема / Topic %d", row.ThreadID)
		}
		status := ""
		if row.Status == "paused" || row.Status == "closed" {
			status = " · Iulita приостановлена / paused"
		}
		if row.Status == "deleting" {
			status = " · завершение удаления / deletion pending"
		}
		if row.BotID == 0 && c.botID > 0 {
			status += fmt.Sprintf(" · старый контекст: /topic bind %d в общем чате / legacy binding required", row.ThreadID)
		}
		fmt.Fprintf(&text, "\n%s%s\nhttps://t.me/%s/%d\n", truncateRunes(name, 128), status, c.linkUsername(), row.ThreadID)
	}
	if more {
		fmt.Fprintf(&text, "\nДалее / Next: /topic list %d\n", page+1)
	}
	text.WriteString("\nСообщение в общем чате начинает новую тему. /clear и /compact действуют в открытой теме.\nA message in the root chat starts a new topic; /clear and /compact affect the current topic.")
	for _, chunk := range channel.VerbatimChunks(text.String(), 4000) {
		c.sendSingleMessage(m.Chat.ID, chunk, m.ID, m.MessageThreadID)
	}
}

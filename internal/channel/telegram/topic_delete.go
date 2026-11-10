package telegram

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-telegram/bot"
	"github.com/iulita-ai/iulita/internal/domain"
)

type topicDeletionStore interface {
	BeginTelegramTopicDeletion(context.Context, domain.TelegramTopicDeletionScope, string) (*domain.TelegramTopicDeletion, error)
	FinalizeTelegramTopicDeletion(context.Context, domain.TelegramTopicDeletionScope) (*domain.TelegramTopicDeletion, error)
	GetTelegramTopicDeletion(context.Context, string) (*domain.TelegramTopicDeletion, error)
	ClaimTelegramTopicDeletionNotice(context.Context, domain.TelegramTopicDeletionScope) (bool, error)
	ListTelegramTopicSyncRoutes(context.Context, string, string, int) ([]domain.TelegramConversationRoute, error)
}

// SetTopicDeletionFunc installs cancellation for a deleted conversation.
func (c *Channel) SetTopicDeletionFunc(fn func(context.Context, string) error) {
	c.topicDeletionFn = fn
}

func (c *Channel) verifyTopicIdentity(ctx context.Context) error {
	if c.botID <= 0 {
		return fmt.Errorf("verified bot identity unavailable")
	}
	me, e := c.bot.GetMe(ctx)
	if e != nil {
		return e
	}
	if me.ID != c.botID || !me.HasTopicsEnabled {
		return fmt.Errorf("topic-capable bot identity unavailable")
	}
	return nil
}

func (c *Channel) deletionScope(ctx context.Context, key string) (domain.TelegramTopicDeletionScope, error) {
	t, e := c.resolveTargetForLifecycle(ctx, key)
	if e != nil || !t.Private || t.BotID <= 0 || t.BotID != c.botID || t.InstanceID != c.instanceID || t.OwnerID == "" || t.ChatID <= 0 || t.ThreadID <= 0 || !c.isAllowed(t.ChatID) {
		return domain.TelegramTopicDeletionScope{}, fmt.Errorf("private topic deletion scope unavailable")
	}
	return domain.TelegramTopicDeletionScope{BotID: t.BotID, ConversationID: key, InstanceID: c.instanceID, OwnerID: t.OwnerID, ChatID: t.ChatID, ThreadID: t.ThreadID}, nil
}

// Only exact, typed Telegram 400 errors from a thread-targeted API call count.
// A message edit failure, blocked bot, timeout, 403 or 429 never proves deletion.
func missingTelegramTopic(err error) bool {
	if !errors.Is(err, bot.ErrorBadRequest) {
		return false
	}
	for e := err; e != nil; e = errors.Unwrap(e) {
		switch e.Error() {
		case "bad request, Bad Request: TOPIC_ID_INVALID", "bad request, Bad Request: message thread not found":
			return true
		}
	}
	return false
}

func missingTopicProbe(err error) bool {
	if !errors.Is(err, bot.ErrorBadRequest) {
		return false
	}
	for e := err; e != nil; e = errors.Unwrap(e) {
		if e.Error() == "bad request, Bad Request: TOPIC_ID_INVALID" {
			return true
		}
	}
	return false
}

func (c *Channel) stopDeletedTopic(ctx context.Context, s domain.TelegramTopicDeletionScope) {
	if c.topicDeletionFn != nil {
		if deliveryErr := c.topicDeletionFn(ctx, s.ConversationID); deliveryErr != nil {
			c.noteFailure("Telegram topicDeletionFn failed", deliveryErr)
		}
	}
	if c.prompts != nil {
		c.prompts.mu.Lock()
		for key, p := range c.prompts.pending {
			if p.chat == s.ChatID && p.thread == s.ThreadID && p.actor == s.ChatID {
				delete(c.prompts.pending, key)
				p.options = nil
				p.otherMode = false
				select {
				case p.replyCh <- "__cancel__":
				default:
				}
			}
		}
		c.prompts.mu.Unlock()
	}
	if c.confirms != nil {
		c.confirms.mu.Lock()
		for key, p := range c.confirms.pending {
			if p.chatID == s.ChatID && p.threadID == s.ThreadID && p.expectFrom == s.ChatID {
				delete(c.confirms.pending, key)
				select {
				case p.replyCh <- "0":
				default:
				}
			}
		}
		c.confirms.mu.Unlock()
	}
	if c.remembers != nil {
		c.remembers.mu.Lock()
		if c.remembers.blocked == nil {
			c.remembers.blocked = map[string]bool{}
		}
		c.remembers.blocked[s.ConversationID] = true
		for key, p := range c.remembers.entries {
			if p.chatID == s.ConversationID && p.userID == s.OwnerID {
				delete(c.remembers.entries, key)
			}
		}
		c.remembers.mu.Unlock()
	}
	if c.statusMsgs != nil {
		c.statusMsgs.forgetConversation(s.ConversationID)
	}
	if c.sentMsgs != nil {
		c.sentMsgs.clearThread(s.ChatID, s.ThreadID)
	}
}

func (c *Channel) notifyTopicDeleted(ctx context.Context, s domain.TelegramTopicDeletionScope, d *domain.TelegramTopicDeletion) {
	store, ok := c.store.(topicDeletionStore)
	if !ok || d == nil {
		return
	}
	claimed, e := store.ClaimTelegramTopicDeletionNotice(ctx, s)
	if e != nil || !claimed {
		return
	}
	prefix := fmt.Sprintf("Тема %d удалена.", s.ThreadID)
	if d.Source != "command" {
		prefix = fmt.Sprintf("Обнаружено удаление темы %d в Telegram. Локальная очистка завершена.", s.ThreadID)
	}
	text := fmt.Sprintf("%s Контекст очищен: %d сообщений; локально отменено напоминаний: %d; отключено фоновых расписаний: %d; локально отменено запусков: %d. Внешние задачи сохранены. Уже отправленные внешние запросы могут завершиться.\nTopic deleted; context cleared, future bound work cancelled locally; external tasks preserved.", prefix, d.Messages, d.Reminders, d.Jobs, d.Tasks)
	// One durable notice attempt. An uncertain send is not automatically replayed.
	if _, deliveryErr := c.bot.SendMessage(ctx, &bot.SendMessageParams{ChatID: s.ChatID, Text: text}); deliveryErr != nil {
		c.noteFailure("Telegram SendMessage failed", deliveryErr)
	}
}

// reconcileGoneTopic is used only after definite native absence/success.
func (c *Channel) reconcileGoneTopic(ctx context.Context, key, source string) error {
	s, e := c.deletionScope(ctx, key)
	if e != nil {
		return e
	}
	store, ok := c.store.(topicDeletionStore)
	if !ok {
		return fmt.Errorf("topic deletion store unavailable")
	}
	if _, e = store.BeginTelegramTopicDeletion(ctx, s, source); e != nil {
		return e
	}
	c.stopDeletedTopic(ctx, s)
	d, e := store.FinalizeTelegramTopicDeletion(ctx, s)
	if e != nil {
		return e
	}
	c.notifyTopicDeleted(ctx, s, d)
	return nil
}

func (c *Channel) executeTopicDeletion(ctx context.Context, key string, op *domain.TelegramTopicOperation) {
	s, e := c.deletionScope(ctx, key)
	store, ok := c.store.(topicDeletionStore)
	if e != nil || !ok {
		c.sendSingleMessage(op.ChatID, "Удаление недоступно: локальная подготовка не выполнена / Local deletion preparation unavailable", 0)
		return
	}
	topics, topicsOK := c.store.(topicStore)
	if !topicsOK {
		return
	}
	intent, e := topics.GetTelegramTopicOperation(ctx, op.ID, s.OwnerID, s.InstanceID)
	if e != nil || intent == nil || intent.Status != "running" || intent.Action != "delete" || intent.Digest != op.Digest || intent.ChatID != s.ChatID || intent.ThreadID != s.ThreadID {
		c.sendSingleMessage(s.ChatID, "Подтверждённая операция удаления недоступна / Approved deletion operation unavailable", 0)
		return
	}
	s.OperationID = op.ID
	s.Digest = op.Digest
	if _, e = store.BeginTelegramTopicDeletion(ctx, s, "command"); e != nil {
		c.sendSingleMessage(s.ChatID, "Не удалось подготовить удаление. Telegram не изменён / Local preparation failed; Telegram unchanged", 0)
		return
	}
	c.stopDeletedTopic(ctx, s)
	deleted, e := c.bot.DeleteForumTopic(ctx, &bot.DeleteForumTopicParams{ChatID: s.ChatID, MessageThreadID: s.ThreadID})
	if e == nil && !deleted {
		e = fmt.Errorf("native deletion unconfirmed")
	}
	if missingTelegramTopic(e) && c.verifyTopicIdentity(ctx) != nil {
		e = fmt.Errorf("native topic absence unconfirmed")
	}
	if e != nil && !missingTelegramTopic(e) {
		op.Status = "unknown_outcome"
		if deliveryErr := topics.SaveTelegramTopicOperation(ctx, op); deliveryErr != nil {
			c.noteFailure("Telegram topic operation receipt persistence failed", deliveryErr)
		}
		c.sendSingleMessage(s.ChatID, "Локальный контекст очищен, связанные работы отменены. Удаление темы в Telegram пока не подтверждено; проверка продолжится автоматически. Внешние задачи сохранены. / Local cleanup committed; Telegram deletion unconfirmed; reconciliation will continue.", 0)
		return
	}
	d, e := store.FinalizeTelegramTopicDeletion(ctx, s)
	if e != nil {
		c.sendSingleMessage(s.ChatID, "Тема удалена в Telegram, завершение локальной очистки ожидает повторной проверки / Native topic deleted; local finalization pending", 0)
		return
	}
	op.Status = "succeeded"
	if deliveryErr := topics.SaveTelegramTopicOperation(ctx, op); deliveryErr != nil {
		c.noteFailure("Telegram topic operation receipt persistence failed", deliveryErr)
	}
	c.notifyTopicDeleted(ctx, s, d)
}

// checkTopicDeletion uses a deliberately invalid fixed custom-emoji ID, never
// a cached title or valid icon. Live Bot API 10.4 evidence: existing =>
// DOCUMENT_INVALID; deleted => TOPIC_ID_INVALID. This is an observed validation
// contract, not a documented read-only get-topic API. Any other result is unknown.
func (c *Channel) checkTopicDeletion(ctx context.Context, key string) error {
	s, e := c.deletionScope(ctx, key)
	if e != nil {
		return e
	}
	store, ok := c.store.(topicDeletionStore)
	if !ok {
		return nil
	}
	d, e := store.GetTelegramTopicDeletion(ctx, key)
	if e != nil {
		return e
	}
	if d != nil && d.Status == "deleted" {
		c.stopDeletedTopic(ctx, s)
		c.notifyTopicDeleted(ctx, s, d)
		return nil
	}
	if d != nil && d.Status == "deleting" && d.Source != "command" {
		return c.reconcileGoneTopic(ctx, key, d.Source)
	}
	if d != nil && d.Status == "deleting" && d.Source == "command" {
		// Replay only the exact approved, durably tombstoned target. Delete is
		// idempotent for this unique thread; no creation/provider work is replayed.
		deleted, deleteErr := c.bot.DeleteForumTopic(ctx, &bot.DeleteForumTopicParams{ChatID: s.ChatID, MessageThreadID: s.ThreadID})
		e = deleteErr
		if (e == nil && deleted) || missingTelegramTopic(e) {
			return c.reconcileGoneTopic(ctx, key, "command")
		}
		return e
	}
	if c.topicProbeDisabled.Load() {
		return nil
	}
	_, e = c.bot.EditForumTopic(ctx, &bot.EditForumTopicParams{ChatID: s.ChatID, MessageThreadID: s.ThreadID, IconCustomEmojiID: "-1"})
	if missingTopicProbe(e) {
		return c.reconcileGoneTopic(ctx, key, "telegram_missing")
	}
	if e == nil {
		// Unexpected success invalidates the observed contract. Stop probes for
		// this instance; never treat success as proof or issue another mutation.
		c.topicProbeDisabled.Store(true)
		c.logger.Warn("Telegram topic verification contract changed; probes disabled")
		return fmt.Errorf("topic verification unavailable")
	}
	return e
}

// ReconcileTopicDeletions checks a bounded keyset batch. The cursor advances on
// unknown outcomes too, so a blocked route cannot starve other owners/topics.
func (c *Channel) ReconcileTopicDeletions(ctx context.Context) error {
	if !c.topicSyncMu.TryLock() {
		return nil
	}
	defer c.topicSyncMu.Unlock()
	store, ok := c.store.(topicDeletionStore)
	if !ok {
		return nil
	}
	identityCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	e := c.verifyTopicIdentity(identityCtx)
	cancel()
	if e != nil {
		return e
	}
	routes, e := store.ListTelegramTopicSyncRoutes(ctx, c.instanceID, c.topicSyncCursor, 20)
	if e != nil {
		return e
	}
	for _, route := range routes {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c.topicSyncCursor = route.ConversationID
		call, cancel := context.WithTimeout(ctx, 3*time.Second)
		e = c.checkTopicDeletion(call, route.ConversationID)
		cancel()
		var flood *bot.TooManyRequestsError
		if errors.As(e, &flood) {
			return e
		}
	}
	if len(routes) < 20 {
		c.topicSyncCursor = ""
	}
	return nil
}

func (c *Channel) runTopicDeletionSync(ctx context.Context) {
	for {
		err := c.ReconcileTopicDeletions(ctx)
		delay := 30 * time.Second
		var flood *bot.TooManyRequestsError
		if errors.As(err, &flood) && flood.RetryAfter > 0 {
			delay = time.Duration(flood.RetryAfter) * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (c *Channel) observeMissingTopic(ctx context.Context, chat int64, thread int, err error) error {
	if thread <= 0 || chat <= 0 || !missingTelegramTopic(err) {
		return err
	}
	key := c.routeKey(chat, thread)
	if c.verifyTopicIdentity(ctx) != nil {
		return err
	}
	if e := c.reconcileGoneTopic(ctx, key, "telegram_missing"); e != nil {
		return fmt.Errorf("topic missing; cleanup pending: %w", e)
	}
	return domain.ErrConversationDeleted
}

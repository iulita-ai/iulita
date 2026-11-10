package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"github.com/iulita-ai/iulita/internal/domain"
)

// FindTelegramTopicAlbum finds a scoped prior seed so album messages share one creation.
func (s *Store) FindTelegramTopicAlbum(ctx context.Context, instance, owner string, chat int64, album string) (*domain.TelegramTopicSeed, error) {
	if album == "" {
		return nil, nil
	}
	seed := new(domain.TelegramTopicSeed)
	e := s.db.NewSelect().Model(seed).Where("instance_id = ? AND owner_id = ? AND chat_id = ? AND album_id = ?", instance, owner, chat, album).Order("message_id ASC").Limit(1).Scan(ctx)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	return seed, e
}

// ClaimTelegramTopicSeed claims the non-idempotent creation stage for an incoming root message.
func (s *Store) ClaimTelegramTopicSeed(ctx context.Context, seed *domain.TelegramTopicSeed) (bool, error) {
	if seed.ID == "" || seed.OwnerID == "" || seed.InstanceID == "" || seed.ChatID <= 0 || seed.MessageID <= 0 {
		return false, fmt.Errorf("invalid topic seed")
	}
	seed.Status = "creating"
	seed.UpdatedAt = time.Now()
	r, e := s.db.NewInsert().Model(seed).On("CONFLICT (id) DO NOTHING").Exec(ctx)
	if e != nil {
		return false, e
	}
	n, e := r.RowsAffected()
	return n == 1, e
}

// TransitionTelegramTopicSeed advances an exact scoped seed stage behind its lifecycle fence.
func (s *Store) TransitionTelegramTopicSeed(ctx context.Context, seed *domain.TelegramTopicSeed, expected, next string) error {
	if !validTopicSeedTransition(expected, next) {
		return fmt.Errorf("invalid topic seed transition")
	}
	return s.withTelegramCoordinatesWrite(ctx, seed.InstanceID, seed.OwnerID, seed.ChatID, seed.ThreadID, func(db bun.IDB) error {
		r, e := db.NewUpdate().Model((*domain.TelegramTopicSeed)(nil)).Set("status = ?", next).Set("thread_id = ?", seed.ThreadID).Set("copy_id = ?", seed.CopyID).Set("updated_at = ?", time.Now()).Where("id = ? AND owner_id = ? AND instance_id = ? AND chat_id = ? AND message_id = ? AND status = ?", seed.ID, seed.OwnerID, seed.InstanceID, seed.ChatID, seed.MessageID, expected).Exec(ctx)
		if e != nil {
			return e
		}
		n, e := r.RowsAffected()
		if e != nil {
			return e
		}
		if n != 1 {
			return fmt.Errorf("topic seed stage unavailable")
		}
		seed.Status = next
		return nil
	})
}

func validTopicSeedTransition(from, to string) bool {
	switch from {
	case "creating":
		return to == "created" || to == "unknown_create"
	case "created":
		return to == "copying"
	case "copying":
		return to == "copied" || to == "unknown_copy"
	case "copied":
		return to == "processing"
	}
	return false
}

// FinishTelegramTopicSeed marks a scoped processing seed processed, cleared or discarded.
func (s *Store) FinishTelegramTopicSeed(ctx context.Context, instance, owner string, chat int64, thread, message int, status string) error {
	if status != "processed" && status != "cleared" && status != "discarded" {
		return fmt.Errorf("invalid topic seed terminal status")
	}
	_, e := s.db.NewUpdate().Model((*domain.TelegramTopicSeed)(nil)).Set("status = ?", status).Set("updated_at = ?", time.Now()).Where("instance_id = ? AND owner_id = ? AND chat_id = ? AND thread_id = ? AND copy_id = ? AND status = ?", instance, owner, chat, thread, message, "processing").Exec(ctx)
	return e
}

// ListTelegramTopics lists only routes observed by this instance for this owner.
// Telegram Bot API has no API to enumerate all existing private topics.
func (s *Store) ListTelegramTopics(ctx context.Context, instance, owner string, chat int64, offset, limit int, botIDs ...int64) ([]domain.TelegramTopicListItem, error) {
	if instance == "" || owner == "" || chat <= 0 || offset < 0 || limit < 1 || limit > 21 {
		return nil, fmt.Errorf("invalid topic list scope")
	}
	var rows []domain.TelegramTopicListItem
	query := s.db.NewSelect().TableExpr("telegram_conversation_routes AS r").ColumnExpr("r.bot_id, r.conversation_id, r.thread_id, COALESCE(t.name, '') AS name, COALESCE(t.status, 'open') AS status").Join("LEFT JOIN telegram_topic_states AS t ON t.conversation_id = r.conversation_id").Where("r.instance_id = ? AND r.owner_id = ? AND r.chat_id = ? AND r.private = ? AND r.thread_id > 0", instance, owner, chat, true).Where("COALESCE(t.status, 'open') <> ?", "deleted").OrderExpr("r.thread_id DESC").Limit(limit).Offset(offset)
	if len(botIDs) > 1 || (len(botIDs) == 1 && botIDs[0] <= 0) {
		return nil, fmt.Errorf("invalid topic bot scope")
	}
	if len(botIDs) == 1 {
		query = query.Where("(r.bot_id = ? OR r.bot_id = 0)", botIDs[0])
	}
	e := query.Scan(ctx, &rows)
	return rows, e
}

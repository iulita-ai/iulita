package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
)

func (s *Store) CreateUser(ctx context.Context, u *domain.User) error {
	u.CreatedAt = time.Now()
	u.UpdatedAt = time.Now()
	_, err := s.db.NewInsert().Model(u).Exec(ctx)
	if err != nil {
		return fmt.Errorf("creating user: %w", err)
	}
	return nil
}

func (s *Store) GetUser(ctx context.Context, id string) (*domain.User, error) {
	u := new(domain.User)
	err := s.db.NewSelect().Model(u).Where("id = ?", id).Scan(ctx)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("getting user: %w", err)
	}
	return u, nil
}

func (s *Store) GetUserByUsername(ctx context.Context, username string) (*domain.User, error) {
	u := new(domain.User)
	err := s.db.NewSelect().Model(u).Where("username = ?", username).Scan(ctx)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("getting user by username: %w", err)
	}
	return u, nil
}

func (s *Store) UpdateUser(ctx context.Context, u *domain.User) error {
	u.UpdatedAt = time.Now()
	_, err := s.db.NewUpdate().Model(u).WherePK().Exec(ctx)
	if err != nil {
		return fmt.Errorf("updating user: %w", err)
	}
	return nil
}

func (s *Store) ListUsers(ctx context.Context) ([]domain.User, error) {
	var users []domain.User
	err := s.db.NewSelect().Model(&users).Order("created_at ASC").Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}
	return users, nil
}

func (s *Store) DeleteUser(ctx context.Context, id string) error {
	_, err := s.db.NewDelete().Model((*domain.User)(nil)).Where("id = ?", id).Exec(ctx)
	if err != nil {
		return fmt.Errorf("deleting user: %w", err)
	}
	return nil
}

// --- Channel bindings ---

func (s *Store) BindChannel(ctx context.Context, uc *domain.UserChannel) error {
	uc.CreatedAt = time.Now()
	_, err := s.db.NewInsert().Model(uc).Exec(ctx)
	if err != nil {
		return fmt.Errorf("binding channel: %w", err)
	}
	return nil
}

func (s *Store) UnbindChannel(ctx context.Context, id int64) error {
	_, err := s.db.NewDelete().Model((*domain.UserChannel)(nil)).Where("id = ?", id).Exec(ctx)
	if err != nil {
		return fmt.Errorf("unbinding channel: %w", err)
	}
	return nil
}

func (s *Store) GetUserByChannel(ctx context.Context, channelType, channelUserID string) (*domain.User, error) {
	var uc domain.UserChannel
	err := s.db.NewSelect().Model(&uc).
		Where("channel_type = ? AND channel_user_id = ? AND enabled = ?", channelType, channelUserID, true).
		Scan(ctx)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("looking up channel binding: %w", err)
	}
	return s.GetUser(ctx, uc.UserID)
}

func (s *Store) ListUserChannels(ctx context.Context, userID string) ([]domain.UserChannel, error) {
	var channels []domain.UserChannel
	err := s.db.NewSelect().Model(&channels).
		Where("user_id = ?", userID).
		Order("created_at ASC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing user channels: %w", err)
	}
	return channels, nil
}

func (s *Store) ListAllChannels(ctx context.Context) ([]domain.UserChannel, error) {
	var channels []domain.UserChannel
	err := s.db.NewSelect().Model(&channels).
		Order("created_at ASC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing all channels: %w", err)
	}
	return channels, nil
}

func (s *Store) UpdateChannel(ctx context.Context, uc *domain.UserChannel) error {
	_, err := s.db.NewUpdate().Model(uc).WherePK().Exec(ctx)
	if err != nil {
		return fmt.Errorf("updating channel: %w", err)
	}
	return nil
}

// GetChannelInstanceIDByChat returns the channel_instance_id for a given chatID (channel_id field).
// Returns empty string if not found.
func (s *Store) GetChannelInstanceIDByChat(ctx context.Context, chatID string) (string, error) {
	var uc domain.UserChannel
	err := s.db.NewSelect().Model(&uc).
		Column("channel_instance_id").
		Where("channel_id = ?", chatID).
		Limit(1).
		Scan(ctx)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", fmt.Errorf("looking up channel instance for chat: %w", err)
	}
	return uc.ChannelInstanceID, nil
}

// UpdateChannelLocale updates the locale for a user channel identified by chatID (channel_id).
func (s *Store) UpdateChannelLocale(ctx context.Context, chatID string, locale string) error {
	_, err := s.db.NewUpdate().
		Model((*domain.UserChannel)(nil)).
		Set("locale = ?", locale).
		Where("channel_id = ?", chatID).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("updating channel locale: %w", err)
	}
	return nil
}

// GetChannelLocale returns the locale for a user channel by channel type and user ID.
func (s *Store) GetChannelLocale(ctx context.Context, channelType, channelUserID string) (string, error) {
	var uc domain.UserChannel
	err := s.db.NewSelect().Model(&uc).
		Column("locale").
		Where("channel_type = ? AND channel_user_id = ? AND enabled = ?", channelType, channelUserID, true).
		Limit(1).
		Scan(ctx)
	if err != nil {
		if err == sql.ErrNoRows {
			return "en", nil
		}
		return "", fmt.Errorf("getting channel locale: %w", err)
	}
	if uc.Locale == "" {
		return "en", nil
	}
	return uc.Locale, nil
}

// GetChannelLocaleByChatID returns the locale for a user channel by chatID.
func (s *Store) GetChannelLocaleByChatID(ctx context.Context, chatID string) (string, error) {
	var uc domain.UserChannel
	err := s.db.NewSelect().Model(&uc).
		Column("locale").
		Where("channel_id = ?", chatID).
		Limit(1).
		Scan(ctx)
	if err != nil {
		if err == sql.ErrNoRows {
			return "en", nil
		}
		return "", fmt.Errorf("getting channel locale by chat ID: %w", err)
	}
	if uc.Locale == "" {
		return "en", nil
	}
	return uc.Locale, nil
}

// GetUserIDs returns distinct user IDs that have data in the system.
func (s *Store) GetUserIDs(ctx context.Context) ([]string, error) {
	var userIDs []string
	err := s.db.NewSelect().
		Model((*domain.User)(nil)).
		Column("id").
		Order("created_at ASC").
		Scan(ctx, &userIDs)
	if err != nil {
		return nil, fmt.Errorf("getting user IDs: %w", err)
	}
	return userIDs, nil
}

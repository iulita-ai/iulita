package channel

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage"
)

// DBUserResolver resolves users from the database.
// If allowRegister is true, unknown users get auto-created.
type DBUserResolver struct {
	store         storage.Repository
	allowRegister bool
	logger        *zap.Logger
}

// NewDBUserResolver creates a new database-backed user resolver.
func NewDBUserResolver(store storage.Repository, allowRegister bool, logger *zap.Logger) *DBUserResolver {
	return &DBUserResolver{
		store:         store,
		allowRegister: allowRegister,
		logger:        logger,
	}
}

// ResolveUser maps a channel identity to an iulita user.
// If the user is not found and registration is enabled, auto-creates the user.
func (r *DBUserResolver) ResolveUser(ctx context.Context, channelType, channelUserID, channelUsername string, chatID string) (string, error) {
	// Look up existing binding.
	user, err := r.store.GetUserByChannel(ctx, channelType, channelUserID)
	if err != nil {
		return "", fmt.Errorf("looking up channel binding: %w", err)
	}
	if user != nil {
		return user.ID, nil
	}

	// User not found — auto-register if allowed.
	if !r.allowRegister {
		return "", fmt.Errorf("user not registered (channel_type=%s, channel_user_id=%s)", channelType, channelUserID)
	}

	// Create new user with a generated username.
	userID := uuid.Must(uuid.NewV7()).String()
	username := channelUsername
	if username == "" {
		username = fmt.Sprintf("%s_%s", channelType, channelUserID)
	}

	// Generate a random password (user can change it later via dashboard).
	hash, err := auth.HashPassword(uuid.New().String())
	if err != nil {
		return "", fmt.Errorf("generating password: %w", err)
	}

	user = &domain.User{
		ID:             userID,
		Username:       username,
		PasswordHash:   hash,
		Role:           domain.RoleRegular,
		DisplayName:    channelUsername,
		MustChangePass: true,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	if err := r.store.CreateUser(ctx, user); err != nil {
		return "", fmt.Errorf("creating auto-registered user: %w", err)
	}

	// Bind the channel.
	ch := &domain.UserChannel{
		UserID:          userID,
		ChannelType:     channelType,
		ChannelID:       chatID,
		ChannelUserID:   channelUserID,
		ChannelUsername: channelUsername,
		Enabled:         true,
	}
	if err := r.store.BindChannel(ctx, ch); err != nil {
		return "", fmt.Errorf("binding channel: %w", err)
	}

	r.logger.Info("auto-registered new user",
		zap.String("user_id", userID),
		zap.String("username", username),
		zap.String("channel_type", channelType),
		zap.String("channel_user_id", channelUserID))

	return userID, nil
}

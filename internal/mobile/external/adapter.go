// Package external resolves private phone-command origins from live server records.
package external

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/mobile/devices"
	"github.com/iulita-ai/iulita/internal/storage"
)

// Sender routes to an exact channel instance; no fallback is permitted.
type Sender interface {
	SendToInstance(context.Context, string, string, string) error
}

// Adapter uses canonical persisted ownership and the exact-instance sender.
type Adapter struct {
	Store  storage.Repository
	Sender Sender
}

// ResolvePrivate rejects groups, ambiguous mappings and disabled channel instances.
func (a Adapter) ResolvePrivate(ctx context.Context, owner, instance, chat string) (devices.CanonicalOrigin, error) {
	var result devices.CanonicalOrigin
	if a.Store == nil {
		return result, devices.ErrUnavailable
	}
	config, err := a.Store.GetChannelInstance(ctx, instance)
	if err != nil || config == nil || !config.Enabled {
		return result, devices.ErrUnavailable
	}
	records, err := a.Store.ListAllChannels(ctx)
	if err != nil {
		return result, devices.ErrUnavailable
	}
	count := 0
	for index := range records {
		r := &records[index]
		if r.ChannelInstanceID != instance || r.ChannelID != chat {
			continue
		}
		count++
		if !r.Enabled || r.UserID != owner || r.ChannelType != config.Type {
			return result, devices.ErrUnavailable
		}
		private := false
		switch config.Type {
		case domain.ChannelTypeTelegram:
			id, parseErr := strconv.ParseInt(chat, 10, 64)
			private = parseErr == nil && id > 0 && chat == r.ChannelUserID
		case domain.ChannelTypeWeb:
			private = chat == "web:"+owner && r.ChannelUserID == owner
		}
		if !private {
			return result, devices.ErrUnavailable
		}
		sum := sha256.Sum256([]byte(fmt.Sprintf("%d|%s|%s|%s|%s|%s|%s|%s|%s", r.ID, r.UserID, r.ChannelInstanceID, r.ChannelID, r.ChannelUserID, r.CreatedAt.String(), config.UpdatedAt.String(), config.Type, config.Config)))
		result = devices.CanonicalOrigin{Owner: owner, Type: config.Type, Instance: instance, Chat: chat, User: r.ChannelUserID, Revision: hex.EncodeToString(sum[:])}
	}
	if count != 1 || result.Owner == "" {
		return result, devices.ErrUnavailable
	}
	return result, nil
}

// SendTemplate revalidates private ownership immediately before exact routing.
func (a Adapter) SendTemplate(ctx context.Context, origin devices.CanonicalOrigin, text string) error {
	current, err := a.ResolvePrivate(ctx, origin.Owner, origin.Instance, origin.Chat)
	if err != nil || current.Revision != origin.Revision || a.Sender == nil {
		return devices.ErrUnavailable
	}
	return a.Sender.SendToInstance(ctx, current.Instance, current.Chat, text)
}

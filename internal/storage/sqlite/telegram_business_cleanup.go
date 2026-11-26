package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"

	business "github.com/iulita-ai/iulita/internal/telegrambusiness"
)

// TelegramBusinessIngressOwner is solely a trusted delete-update routing lookup.
// It exposes no content and does not grant user or outbound capability. It stays
// available after owner disable/unbind so known-scope deletion can still purge.
func (s *Store) TelegramBusinessIngressOwner(ctx context.Context, key business.Key) (string, error) {
	if !validBusinessKey(key) {
		return "", business.ErrDenied
	}
	var owner string
	err := s.db.NewRaw(`SELECT owner_id FROM telegram_business_connections WHERE instance_id=? AND bot_id=? AND connection_id=?`, key.InstanceID, key.BotID, key.ConnectionID).Scan(ctx, &owner)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && owner == "") {
		return "", business.ErrDenied
	}
	return owner, err
}

// KnownKeys enumerates bounded exact-bot metadata for startup admission fencing.
// It cannot return content or infer ownership from a replacement bot token.
func (r *TelegramBusinessRepository) KnownKeys(ctx context.Context, instance string, bot int64) ([]business.Key, error) {
	if !validBusinessPoll(instance, bot) {
		return nil, business.ErrDenied
	}
	var connections []string
	if err := r.store.db.NewRaw(`SELECT connection_id FROM telegram_business_connections WHERE instance_id=? AND bot_id=? ORDER BY connection_id LIMIT 101`, instance, bot).Scan(ctx, &connections); err != nil {
		return nil, err
	}
	if len(connections) > 100 {
		return nil, business.ErrQuota
	}
	keys := make([]business.Key, 0, len(connections))
	for _, id := range connections {
		keys = append(keys, business.Key{InstanceID: instance, BotID: bot, ConnectionID: id})
	}
	return keys, nil
}

// ApplyTelegramBusinessDeletion performs only bounded, exact-scope erasure from
// authenticated Telegram delete events. No general callback/authority bypass is
// exposed: disabled/revoked owners can lose content but gain no capability.
func (r *TelegramBusinessRepository) ApplyTelegramBusinessDeletion(ctx context.Context, key business.Key, owner string, peerID int64, ids []int, at time.Time) error {
	if !validBusinessKey(key) || owner == "" || peerID <= 0 || len(ids) == 0 || len(ids) > 100 || at.IsZero() {
		return business.ErrDenied
	}
	deleted := make(map[int]bool, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return business.ErrDenied
		}
		deleted[id] = true
	}
	return r.update(ctx, key, func(state *business.State) error {
		business.PruneState(state, at)
		if state.Peers == nil {
			state.Peers = make(map[int64]*business.Peer)
		}
		peer := state.Peers[peerID]
		if peer == nil {
			if len(state.Peers) >= 100 {
				return business.ErrQuota
			}
			peer = &business.Peer{ID: peerID, Policy: business.PeerPolicy{Mode: business.ModeOff}}
			state.Peers[peerID] = peer
		}
		changed := false
		for i := range peer.Messages {
			message := &peer.Messages[i]
			if deleted[message.ID] {
				changed = changed || !message.Deleted || message.Text != "" || len(message.Media) > 0 || message.MediaGroupID != "" || message.ExtractedText != "" || message.ExtractedProvider != ""
				message.Text = ""
				message.Media = nil
				message.MediaGroupID = ""
				message.ExtractedText = ""
				message.ExtractedProvider = ""
				message.Deleted = true
				delete(deleted, message.ID)
			}
		}
		for id := range deleted {
			peer.Messages = append(peer.Messages, business.Message{ID: id, Deleted: true, Date: at})
			changed = true
		}
		if !changed {
			return nil
		}
		if len(peer.Messages) > 1000 {
			peer.Messages = nil
			peer.ReplayAfter = at
			peer.Policy.Enabled = false
		}
		if peer.Revision == math.MaxUint64 {
			return business.ErrQuota
		}
		peer.Revision++
		peer.Summary = nil
		for _, draft := range peer.Drafts {
			if draft != nil {
				draft.Text = ""
				draft.State = "cancelled"
			}
		}
		for _, action := range peer.Actions {
			if action != nil {
				action.Params = nil
				if action.Receipt != nil {
					action.Receipt.Result = nil
				}
				cancelBusinessAction(action)
			}
		}
		return nil
	}, owner)
}

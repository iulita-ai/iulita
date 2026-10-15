package devices

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/mobile/browseroauth"
	"github.com/iulita-ai/iulita/internal/mobile/conversations"
	"github.com/iulita-ai/iulita/internal/mobile/engine"
	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// CanonicalOrigin is resolved from current account/channel records, not message text.
type CanonicalOrigin struct{ Owner, Type, Instance, Chat, User, Revision string }

// OriginResolver checks exact private routing and sends metadata-only templates.
type OriginResolver interface {
	ResolvePrivate(context.Context, string, string, string) (CanonicalOrigin, error)
	SendTemplate(context.Context, CanonicalOrigin, string) error
}
type externalBinding struct {
	ID, Owner, Device, Viewer, Conversation, Profile, Revision string
	Origin                                                     CanonicalOrigin
	Authority                                                  oauthstore.Authority
	Expiry                                                     time.Time
	Revoked                                                    bool
}

func (s *Store) loadBinding(ctx context.Context, tx *sql.Tx, owner, id string) (externalBinding, error) {
	var b externalBinding
	var data []byte
	var expiry int64
	err := tx.QueryRowContext(ctx, `SELECT id,owner_id,device_id,viewer_device_id,channel_type,instance,chat_id,conversation_id,profile_id,authority,canonical_revision,expires_at,revoked FROM mobile_external_bindings WHERE owner_id=? AND id=?`, owner, id).Scan(&b.ID, &b.Owner, &b.Device, &b.Viewer, &b.Origin.Type, &b.Origin.Instance, &b.Origin.Chat, &b.Conversation, &b.Profile, &data, &b.Revision, &expiry, &b.Revoked)
	if err != nil || json.Unmarshal(data, &b.Authority) != nil {
		return b, ErrUnavailable
	}
	b.Expiry = time.UnixMilli(expiry)
	b.Origin.Owner = b.Owner
	b.Origin.Revision = b.Revision
	return b, nil
}
func (s *Store) bindingLive(ctx context.Context, tx *sql.Tx, b externalBinding) error {
	if s.options.Origins == nil || b.Revoked || !b.Expiry.After(time.Now()) || s.auth.ValidateAuthorityIn(ctx, tx, b.Authority, b.Owner) != nil {
		return ErrUnavailable
	}
	current, err := s.options.Origins.ResolvePrivate(ctx, b.Owner, b.Origin.Instance, b.Origin.Chat)
	if err != nil || current.Owner != b.Owner || current.Type != b.Origin.Type || current.Revision != b.Revision || current.Chat != b.Origin.Chat || current.Instance != b.Origin.Instance {
		return ErrUnavailable
	}
	return nil
}
func (s *Store) externalLive(ctx context.Context, tx *sql.Tx, t Task) error {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT binding_id FROM mobile_external_turns WHERE turn_id=?`, t.Turn).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	b, err := s.loadBinding(ctx, tx, t.Owner, id)
	if err != nil || b.Device != t.Device || b.Viewer != t.ViewerDevice {
		return ErrUnavailable
	}
	return s.bindingLive(ctx, tx, b)
}
func (s *Store) bindTurnIn(ctx context.Context, tx *sql.Tx, owner, turn, id, device string) error {
	b, err := s.loadBinding(ctx, tx, owner, id)
	if err != nil || b.Device != device {
		return ErrUnavailable
	}
	if stepErr1 := s.bindingLive(ctx, tx, b); stepErr1 != nil {
		return stepErr1
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO mobile_external_turns VALUES(?,?) ON CONFLICT(turn_id) DO NOTHING`, turn, id)
	return err
}
func wireBinding(b externalBinding) *v1.ExternalOriginBinding {
	return &v1.ExternalOriginBinding{BindingId: b.ID, ChannelType: b.Origin.Type, ChannelInstanceId: b.Origin.Instance, ChatId: b.Origin.Chat, DeviceId: b.Device, ExpiresAt: timestamppb.New(b.Expiry), Revoked: b.Revoked}
}

// BindExternal installs an expiring private-origin link with no bearer token.
func (s *Store) BindExternal(ctx context.Context, identity Identity, in *v1.BindExternalOriginRequest) (*v1.ExternalOriginBinding, error) {
	if in == nil || in.Operation == nil || !validID(in.Operation.OperationId) || identity.Claims == nil || s.options.Origins == nil {
		return nil, ErrInvalid
	}
	conversation, err := s.chats.Get(ctx, identity.Claims.Subject, in.ConversationId)
	if err != nil || conversation.Settings.ProfileID != in.ProfileId {
		return nil, ErrUnavailable
	}
	var out *v1.ExternalOriginBinding
	err = s.with(ctx, identity, func(tx *sql.Tx, c *oauthstore.Claims) error {
		if !oauthstore.HasScope(c, "devices:manage") || !s.auth.Fresh(c) {
			return ErrUnavailable
		}
		canonical, stepErr2 := s.options.Origins.ResolvePrivate(ctx, c.Subject, in.ChannelInstanceId, in.ChatId)
		if stepErr2 != nil || canonical.Owner != c.Subject || canonical.Revision == "" || canonical.Instance != in.ChannelInstanceId || canonical.Chat != in.ChatId || (canonical.Type != "telegram" && canonical.Type != "web") {
			return ErrUnavailable
		}
		var count int
		if tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM oauth_devices WHERE owner_id=? AND id=? AND activated=1 AND revoked=0`, c.Subject, in.DeviceId).Scan(&count) != nil || count != 1 {
			return ErrUnavailable
		}
		existing, lookupErr := s.loadBinding(ctx, tx, c.Subject, in.Operation.OperationId)
		if lookupErr == nil {
			if existing.Device != in.DeviceId || existing.Viewer != c.DeviceID || existing.Origin.Instance != canonical.Instance || existing.Origin.Chat != canonical.Chat || existing.Conversation != in.ConversationId || existing.Profile != in.ProfileId {
				return ErrInvalid
			}
			out = wireBinding(existing)
			return nil
		}
		if stepErr3 := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mobile_external_bindings WHERE owner_id=? AND expires_at>? AND revoked=0`, c.Subject, time.Now().UnixMilli()).Scan(&count); stepErr3 != nil {
			return stepErr3
		}
		if count >= 16 {
			return ErrBusy
		}
		b := externalBinding{ID: in.Operation.OperationId, Owner: c.Subject, Device: in.DeviceId, Viewer: c.DeviceID, Conversation: in.ConversationId, Profile: in.ProfileId, Origin: canonical, Revision: canonical.Revision, Authority: s.auth.Authority(c), Expiry: expiryBound(time.Now().Add(15*time.Minute), c.ExpiresAt.Time)}
		authority, stepErr2 := metadata(b.Authority)
		if stepErr2 != nil {
			return stepErr2
		}
		_, stepErr2 = tx.ExecContext(ctx, `INSERT INTO mobile_external_bindings VALUES(?,?,?,?,?,?,?,?,?,?,?,?,0)`, b.ID, b.Owner, b.Device, b.Viewer, b.Origin.Type, b.Origin.Instance, b.Origin.Chat, b.Conversation, b.Profile, authority, b.Revision, b.Expiry.UnixMilli())
		out = wireBinding(b)
		return stepErr2
	})
	return out, err
}

// RevokeExternal closes the binding before future command dispatch or result reads.
func (s *Store) RevokeExternal(ctx context.Context, identity Identity, in *v1.RevokeExternalOriginRequest) (*v1.ExternalOriginBinding, error) {
	if in == nil || in.Operation == nil || !validID(in.Operation.OperationId) || !validID(in.BindingId) {
		return nil, ErrInvalid
	}
	id := in.BindingId
	digest, digestErr := semantic(&v1.RevokeExternalOriginRequest{BindingId: id})
	if digestErr != nil {
		return nil, digestErr
	}
	var out *v1.ExternalOriginBinding
	err := s.with(ctx, identity, func(tx *sql.Tx, c *oauthstore.Claims) error {
		if err := scope(c, "devices:manage"); err != nil {
			return err
		}
		var prior []byte
		var device string
		lookup := tx.QueryRowContext(ctx, `SELECT digest,device_id FROM mobile_phone_operations WHERE owner_id=? AND client_id=? AND operation_id=? AND kind='revoke_origin'`, c.Subject, c.ClientID, in.Operation.OperationId).Scan(&prior, &device)
		if lookup == nil && (!bytes.Equal(prior, digest) || device != c.DeviceID) {
			return ErrInvalid
		}
		if lookup != nil && !errors.Is(lookup, sql.ErrNoRows) {
			return lookup
		}
		b, err := s.loadBinding(ctx, tx, c.Subject, id)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE mobile_external_bindings SET revoked=1 WHERE owner_id=? AND id=?`, c.Subject, id); err != nil {
			return err
		}
		if errors.Is(lookup, sql.ErrNoRows) {
			if _, err := tx.ExecContext(ctx, `INSERT INTO mobile_phone_operations VALUES(?,?,?,'revoke_origin',?,?,'revoked',?,?,?)`, c.Subject, c.ClientID, in.Operation.OperationId, c.DeviceID, digest, id, b.Expiry.UnixMilli(), time.Now().UnixMilli()); err != nil {
				return err
			}
		}
		b.Revoked = true
		out = wireBinding(b)
		return nil
	})
	return out, err
}

// HandleExternal intercepts only explicit /phone commands before ordinary history.
func (s *Store) HandleExternal(ctx context.Context, processor *engine.Engine, msg channel.IncomingMessage) (response string, handled bool, err error) {
	if !strings.HasPrefix(msg.Text, "/phone") {
		return "", false, nil
	}
	if s.options.Origins == nil || msg.ResolvedUserID == "" || msg.ChannelInstanceID == "" || (msg.MessageID <= 0 && !validID(msg.ClientMessageID)) {
		return externalUnavailable()
	}
	canonical, err := s.options.Origins.ResolvePrivate(ctx, msg.ResolvedUserID, msg.ChannelInstanceID, msg.ChatID)
	if err != nil || canonical.User != msg.UserID {
		return externalUnavailable()
	}
	var b externalBinding
	err = s.withExternalAccount(ctx, msg.ResolvedUserID, func(ctx context.Context) error {
		tx, stepErr4 := s.auth.Transaction(ctx)
		if stepErr4 != nil {
			return stepErr4
		}
		var id string
		if tx.QueryRowContext(ctx, `SELECT id FROM mobile_external_bindings WHERE owner_id=? AND instance=? AND chat_id=? AND revoked=0 AND expires_at>? ORDER BY expires_at DESC LIMIT 1`, msg.ResolvedUserID, msg.ChannelInstanceID, msg.ChatID, time.Now().UnixMilli()).Scan(&id) != nil {
			return ErrUnavailable
		}
		b, stepErr4 = s.loadBinding(ctx, tx, msg.ResolvedUserID, id)
		if stepErr4 != nil {
			return stepErr4
		}
		return s.bindingLive(ctx, tx, b)
	})
	if err != nil {
		return externalUnavailable()
	}
	parts := strings.Fields(msg.Text)
	if len(parts) != 2 {
		return "Команды: /phone status, /phone location, /phone notifications. Результат доступен в мобильном Iulita.", true, nil
	}
	tool, ok := map[string]string{"status": "phone_status", "location": "phone_location", "notifications": "phone_notifications"}[parts[1]]
	if !ok {
		return "Неизвестная команда телефона.", true, nil
	}
	conversation, err := s.chats.Get(ctx, b.Owner, b.Conversation)
	if err != nil || conversation.Settings.ProfileID != b.Profile {
		return externalUnavailable()
	}
	guarded := conversations.WithVerifier(ctx, func(check context.Context) error {
		return s.auth.WithRequest(check, func(inner context.Context) error {
			tx, stepErr5 := s.auth.Transaction(inner)
			if stepErr5 != nil {
				return stepErr5
			}
			return s.bindingLive(inner, tx, b)
		})
	})
	guarded = engine.WithExternalBinding(engine.WithAuthority(guarded, b.Authority), b.ID)
	input := "Request " + tool + " for device_id " + b.Device + ". Return task metadata only."
	messageID := msg.ClientMessageID
	if msg.MessageID > 0 {
		messageID = strconv.Itoa(msg.MessageID)
	}
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(b.ID+":"+messageID)).String()
	_, _, err = processor.Accept(guarded, b.Owner, b.Conversation, conversations.Input{ClientMessageID: id, Text: input, Generation: conversation.Generation, TargetDeviceID: b.Device})
	if err != nil {
		return externalUnavailable()
	}
	return "Запрос телефона принят. Подтверждение и результат доступны в мобильном Iulita.", true, nil
}

// PublishExternal sends fixed task-state templates through an exact private link.
// A send is claimed before network I/O: losing its ACK never repeats an action.
func (s *Store) PublishExternal(ctx context.Context) error {
	if s.options.Origins == nil {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT t.id,t.owner_id,x.binding_id,t.state FROM mobile_phone_tasks t JOIN mobile_external_turns x ON x.turn_id=t.turn_id JOIN mobile_external_bindings b ON b.id=x.binding_id LEFT JOIN mobile_external_delivery d ON d.task_id=t.id WHERE COALESCE(d.last_state,'')<>t.state AND b.revoked=0 AND b.expires_at>? ORDER BY t.created_at,t.id LIMIT 16`, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	defer rows.Close() //nolint:errcheck // Early failures preserve their error; normal close is checked below.
	type candidate struct{ task, owner, binding, state string }
	var candidates []candidate
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.task, &item.owner, &item.binding, &item.state); err != nil {
			_ = rows.Close() //nolint:errcheck // Preserve the scan failure.
			return err
		}
		candidates = append(candidates, item)
	}
	rowErr := rows.Err()
	closeErr := rows.Close()
	if rowErr != nil {
		return rowErr
	}
	if closeErr != nil {
		return closeErr
	}
	for _, item := range candidates {
		// Fixed metadata is lossy and at most once per observed transition.
		// Claim before validation so dead/remapped origins cannot monopolize the
		// bounded candidate batch. This claim grants no task or origin authority.
		claim, claimErr := s.db.ExecContext(ctx, `INSERT INTO mobile_external_delivery(task_id,last_state) SELECT id,state FROM mobile_phone_tasks WHERE id=? AND owner_id=? AND state=? ON CONFLICT(task_id) DO UPDATE SET last_state=excluded.last_state WHERE mobile_external_delivery.last_state<>excluded.last_state`, item.task, item.owner, item.state)
		if claimErr != nil {
			return claimErr
		}
		claimed, claimErr := claim.RowsAffected()
		if claimErr != nil {
			return claimErr
		}
		if claimed != 1 {
			continue
		}
		var binding externalBinding
		var template string
		guardErr := s.withExternalAccount(ctx, item.owner, func(inner context.Context) error {
			tx, err := s.auth.Transaction(inner)
			if err != nil {
				return err
			}
			binding, err = s.loadBinding(inner, tx, item.owner, item.binding)
			if err != nil {
				return err
			}
			if stepErr6 := s.bindingLive(inner, tx, binding); stepErr6 != nil {
				return stepErr6
			}
			t, err := taskIn(inner, tx, item.owner, item.task)
			if err != nil {
				return err
			}
			if t.State != item.state {
				return ErrUnavailable
			}
			template = "Задача телефона " + t.ID + ": " + t.State + ". Данные доступны только в мобильном Iulita."
			return nil
		})
		if guardErr != nil {
			continue
		}
		if err := s.options.Origins.SendTemplate(ctx, binding.Origin, template); err != nil {
			continue
		}
	}
	return nil
}

// externalUnavailable deliberately translates every private-origin diagnostic to
// a fixed user response. It never exposes identifiers, account state or DB errors.
func externalUnavailable() (response string, handled bool, err error) {
	return "Откройте Iulita на телефоне и привяжите этот личный чат.", true, nil
}

// Preserve account-to-OAuth lock order even when no mobile API request has
// synchronized a recent password change or account security logout yet.
func (s *Store) withExternalAccount(ctx context.Context, owner string, fn func(context.Context) error) error {
	return s.accounts.Guard(ctx, owner, func(account browseroauth.Account) error {
		available := account.ID == owner && !account.Disabled && !account.MustChangePassword
		if err := s.auth.SyncAccount(ctx, owner, account.CredentialVersion, available); err != nil {
			return err
		}
		if !available {
			return ErrUnavailable
		}
		return s.auth.WithRequest(ctx, fn)
	})
}
